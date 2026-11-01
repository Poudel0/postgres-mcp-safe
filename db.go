package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type limits struct {
	timeout  time.Duration
	maxRows  int
	maxBytes int
}

type db struct {
	cfg *pgx.ConnConfig
	limits
	allowSuperuser bool
	warnSuperuser  sync.Once
}

func newDB(cfg *pgx.ConnConfig, l limits, allowSuperuser bool) *db {
	cfg.MaxProtocolMessageBodyLen = l.maxBytes
	return &db{cfg: cfg, limits: l, allowSuperuser: allowSuperuser}
}

// clientGrace lets Postgres's own statement_timeout fire first, with a clear
// error; the client deadline is the backstop SQL can't turn off.
const clientGrace = time.Second

var (
	errNoResultColumns = errors.New("only statements that return rows are allowed (SELECT, WITH, VALUES, TABLE, SHOW, EXPLAIN)")
	errSuperuser       = errors.New("refusing to run as a superuser (or a member of one): superusers can get around a read-only transaction, e.g. lo_export writes files on the database server. Connect as a read-only role (see README), or start the server with --allow-superuser")
)

// Membership, not just rolsuper: a member of a superuser role can
// set_config('role', ...) part-way through a statement.
const setupSQL = `SELECT
  EXISTS (SELECT FROM pg_roles WHERE rolsuper AND pg_has_role(current_user, oid, 'MEMBER')),
  set_config('statement_timeout', $1, true)`

// readOnly is the only way to touch the database: a fresh connection and a
// READ ONLY transaction, both gone when fn returns.
func (d *db) readOnly(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout+clientGrace)
	defer cancel()

	conn, err := pgx.ConnectConfig(ctx, d.cfg)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	// Closing ends the transaction too, so there is no separate Rollback.
	defer conn.Close(ctx)

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	var superuser bool
	ms := strconv.FormatInt(d.timeout.Milliseconds(), 10)
	if err := tx.QueryRow(ctx, setupSQL, ms).Scan(&superuser, nil); err != nil {
		return fmt.Errorf("session setup: %w", err)
	}
	if superuser {
		if !d.allowSuperuser {
			return errSuperuser
		}
		d.warnSuperuser.Do(func() {
			slog.Warn("running as a superuser (--allow-superuser): read-only is not guaranteed", "user", d.cfg.User)
		})
	}
	return fn(ctx, tx)
}

type result struct {
	json      string // 2-space indented JSON array of row objects
	truncated string // why rows were cut off; empty if none were
}

// userQuery runs untrusted SQL. It only ever reaches Postgres as a Parse
// message, which holds at most one statement, and it runs only if Describe
// says it returns rows.
func (d *db) userQuery(ctx context.Context, sql string) (r result, err error) {
	// Also a safety check: pgx sends "" over the simple protocol.
	if strings.TrimSpace(sql) == "" {
		return result{}, errors.New("sql is required")
	}
	err = d.readOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		sd, err := tx.Prepare(ctx, "q", sql)
		if err != nil {
			return fmt.Errorf("prepare: %w", err)
		}
		if len(sd.Fields) == 0 {
			return errNoResultColumns
		}
		r, err = d.readRows(ctx, tx, "q")
		return err
	})
	return r, err
}

// fixedQuery runs our own SQL; user input only goes in args.
func (d *db) fixedQuery(ctx context.Context, sql string, args ...any) (r result, err error) {
	err = d.readOnly(ctx, func(ctx context.Context, tx pgx.Tx) (err error) {
		r, err = d.readRows(ctx, tx, sql, args...)
		return err
	})
	return r, err
}

// readRows encodes the rows under the row and byte caps.
func (d *db) readRows(ctx context.Context, tx pgx.Tx, sql string, args ...any) (result, error) {
	rows, err := tx.Query(ctx, sql, append([]any{pgx.QueryResultFormats{pgx.TextFormatCode}}, args...)...)
	if err != nil {
		return result{}, fmt.Errorf("query: %w", err)
	}
	// No rows.Close on early return: draining the rest of a huge result is
	// what the caps exist to avoid. readOnly drops the connection instead.
	var items []string
	size := len("[]")
	for rows.Next() {
		if len(items) == d.maxRows {
			return result{joinRows(items), fmt.Sprintf("Result truncated to the first %d rows (--max-rows).", d.maxRows)}, nil
		}
		row, err := encodeRow(rows.FieldDescriptions(), rows.RawValues())
		if err != nil {
			return result{}, err
		}
		if size+len(row)+len(",\n  ") > d.maxBytes {
			return result{joinRows(items), fmt.Sprintf("Result truncated after %d rows: output would exceed %d bytes (--max-bytes).", len(items), d.maxBytes)}, nil
		}
		size += len(row) + len(",\n  ")
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return result{}, fmt.Errorf("query: %w", err)
	}
	return result{json: joinRows(items)}, nil
}

// joinRows matches JSON.stringify(rows, null, 2).
func joinRows(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	return "[\n  " + strings.Join(items, ",\n  ") + "\n]"
}

func encodeRow(fields []pgconn.FieldDescription, raw [][]byte) (string, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(jsonString(f.Name))
		b.WriteByte(':')
		b.Write(encodeValue(f.DataTypeOID, raw[i]))
	}
	b.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, b.Bytes(), "  ", "  "); err != nil {
		return "", fmt.Errorf("encode row: %w", err)
	}
	return out.String(), nil
}

// encodeValue maps Postgres text output to JSON. See SPEC §5, difference 4.
func encodeValue(oid uint32, text []byte) []byte {
	if text == nil {
		return []byte("null")
	}
	switch oid {
	case pgtype.BoolOID:
		return []byte(strconv.FormatBool(string(text) == "t"))
	case pgtype.Int2OID, pgtype.Int4OID, pgtype.Int8OID, pgtype.OIDOID,
		pgtype.Float4OID, pgtype.Float8OID, pgtype.NumericOID:
		if json.Valid(text) { // false for NaN and Infinity
			return text
		}
	case pgtype.JSONOID, pgtype.JSONBOID:
		return text
	}
	return jsonString(string(text))
}

// jsonString encodes like JSON.stringify: no < escaping of <, > and &.
func jsonString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s) // a string always encodes
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
