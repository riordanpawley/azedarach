package tracesqlite

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"strings"

	"github.com/riordanpawley/azedarach/internal/latencytrace"
	sqlite "modernc.org/sqlite"
)

const DriverName = "azedarach-sqlite"

func init() {
	sql.Register(DriverName, Driver{})
}

// Open returns a database handle using the traced SQLite driver.
func Open(dsn string) (*sql.DB, error) {
	return sql.Open(DriverName, dsn)
}

type Driver struct {
	inner sqlite.Driver
}

func (d Driver) Open(name string) (sqldriver.Conn, error) {
	ctx, endSpan := latencytrace.StartSpan(context.Background(), "dependency", "sqlite.open",
		"dependency.name", "sqlite",
		"dependency.operation", "open",
	)
	conn, err := d.inner.Open(name)
	endSpan(err)
	if err != nil {
		return nil, err
	}
	return tracedConn{Conn: conn, ctx: ctx}, nil
}

type tracedConn struct {
	sqldriver.Conn
	ctx context.Context
}

func (c tracedConn) Prepare(query string) (sqldriver.Stmt, error) {
	ctx, endSpan := latencytrace.StartSpan(c.ctx, "dependency", "sqlite.prepare",
		"dependency.name", "sqlite",
		"dependency.operation", sqlOperation(query),
	)
	stmt, err := c.Conn.Prepare(query)
	endSpan(err)
	if err != nil {
		return nil, err
	}
	return tracedStmt{Stmt: stmt, ctx: ctx, operation: sqlOperation(query)}, nil
}

func (c tracedConn) Begin() (sqldriver.Tx, error) {
	ctx, endSpan := latencytrace.StartSpan(c.ctx, "dependency", "sqlite.begin",
		"dependency.name", "sqlite",
		"dependency.operation", "begin",
	)
	tx, err := c.Conn.Begin()
	endSpan(err)
	if err != nil {
		return nil, err
	}
	return tracedTx{Tx: tx, ctx: ctx}, nil
}

func (c tracedConn) PrepareContext(ctx context.Context, query string) (sqldriver.Stmt, error) {
	ctx, endSpan := latencytrace.StartSpan(ctx, "dependency", "sqlite.prepare",
		"dependency.name", "sqlite",
		"dependency.operation", sqlOperation(query),
	)
	prepare, ok := c.Conn.(sqldriver.ConnPrepareContext)
	if !ok {
		endSpan(sqldriver.ErrSkip)
		return nil, sqldriver.ErrSkip
	}
	stmt, err := prepare.PrepareContext(ctx, query)
	endSpan(err)
	if err != nil {
		return nil, err
	}
	return tracedStmt{Stmt: stmt, ctx: ctx, operation: sqlOperation(query)}, nil
}

func (c tracedConn) ExecContext(ctx context.Context, query string, args []sqldriver.NamedValue) (sqldriver.Result, error) {
	ctx, endSpan := latencytrace.StartSpan(ctx, "dependency", "sqlite.exec",
		"dependency.name", "sqlite",
		"dependency.operation", sqlOperation(query),
	)
	converted, err := convertNamedValues(args)
	if err != nil {
		endSpan(err)
		return nil, err
	}
	exec, ok := c.Conn.(sqldriver.ExecerContext)
	if !ok {
		endSpan(sqldriver.ErrSkip)
		return nil, sqldriver.ErrSkip
	}
	result, err := exec.ExecContext(ctx, query, converted)
	endSpan(err)
	return result, err
}

func (c tracedConn) QueryContext(ctx context.Context, query string, args []sqldriver.NamedValue) (sqldriver.Rows, error) {
	ctx, endSpan := latencytrace.StartSpan(ctx, "dependency", "sqlite.query",
		"dependency.name", "sqlite",
		"dependency.operation", sqlOperation(query),
	)
	converted, err := convertNamedValues(args)
	if err != nil {
		endSpan(err)
		return nil, err
	}
	queryer, ok := c.Conn.(sqldriver.QueryerContext)
	if !ok {
		endSpan(sqldriver.ErrSkip)
		return nil, sqldriver.ErrSkip
	}
	rows, err := queryer.QueryContext(ctx, query, converted)
	endSpan(err)
	return rows, err
}

func (c tracedConn) BeginTx(ctx context.Context, opts sqldriver.TxOptions) (sqldriver.Tx, error) {
	ctx, endSpan := latencytrace.StartSpan(ctx, "dependency", "sqlite.begin",
		"dependency.name", "sqlite",
		"dependency.operation", "begin",
		"readonly", opts.ReadOnly,
	)
	begin, ok := c.Conn.(sqldriver.ConnBeginTx)
	if !ok {
		endSpan(sqldriver.ErrSkip)
		return nil, sqldriver.ErrSkip
	}
	tx, err := begin.BeginTx(ctx, opts)
	endSpan(err)
	if err != nil {
		return nil, err
	}
	return tracedTx{Tx: tx, ctx: ctx}, nil
}

func (c tracedConn) Ping(ctx context.Context) error {
	ctx, endSpan := latencytrace.StartSpan(ctx, "dependency", "sqlite.ping",
		"dependency.name", "sqlite",
		"dependency.operation", "ping",
	)
	pinger, ok := c.Conn.(sqldriver.Pinger)
	if !ok {
		endSpan(sqldriver.ErrSkip)
		return sqldriver.ErrSkip
	}
	err := pinger.Ping(ctx)
	endSpan(err)
	return err
}

func (c tracedConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.Conn.(sqldriver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c tracedConn) IsValid() bool {
	if validator, ok := c.Conn.(sqldriver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

type tracedStmt struct {
	sqldriver.Stmt
	ctx       context.Context
	operation string
}

func (s tracedStmt) Exec(args []sqldriver.Value) (sqldriver.Result, error) {
	_, endSpan := latencytrace.StartSpan(s.ctx, "dependency", "sqlite.stmt_exec",
		"dependency.name", "sqlite",
		"dependency.operation", s.operation,
	)
	result, err := s.Stmt.Exec(args)
	endSpan(err)
	return result, err
}

func (s tracedStmt) Query(args []sqldriver.Value) (sqldriver.Rows, error) {
	_, endSpan := latencytrace.StartSpan(s.ctx, "dependency", "sqlite.stmt_query",
		"dependency.name", "sqlite",
		"dependency.operation", s.operation,
	)
	rows, err := s.Stmt.Query(args)
	endSpan(err)
	return rows, err
}

func (s tracedStmt) ExecContext(ctx context.Context, args []sqldriver.NamedValue) (sqldriver.Result, error) {
	ctx, endSpan := latencytrace.StartSpan(ctx, "dependency", "sqlite.stmt_exec",
		"dependency.name", "sqlite",
		"dependency.operation", s.operation,
	)
	converted, err := convertNamedValues(args)
	if err != nil {
		endSpan(err)
		return nil, err
	}
	exec, ok := s.Stmt.(sqldriver.StmtExecContext)
	if !ok {
		endSpan(sqldriver.ErrSkip)
		return nil, sqldriver.ErrSkip
	}
	result, err := exec.ExecContext(ctx, converted)
	endSpan(err)
	return result, err
}

func (s tracedStmt) QueryContext(ctx context.Context, args []sqldriver.NamedValue) (sqldriver.Rows, error) {
	ctx, endSpan := latencytrace.StartSpan(ctx, "dependency", "sqlite.stmt_query",
		"dependency.name", "sqlite",
		"dependency.operation", s.operation,
	)
	converted, err := convertNamedValues(args)
	if err != nil {
		endSpan(err)
		return nil, err
	}
	query, ok := s.Stmt.(sqldriver.StmtQueryContext)
	if !ok {
		endSpan(sqldriver.ErrSkip)
		return nil, sqldriver.ErrSkip
	}
	rows, err := query.QueryContext(ctx, converted)
	endSpan(err)
	return rows, err
}

func (s tracedStmt) ColumnConverter(idx int) sqldriver.ValueConverter {
	if converter, ok := s.Stmt.(sqldriver.ColumnConverter); ok {
		return converter.ColumnConverter(idx)
	}
	return sqldriver.DefaultParameterConverter
}

type tracedTx struct {
	sqldriver.Tx
	ctx context.Context
}

func (t tracedTx) Commit() error {
	_, endSpan := latencytrace.StartSpan(t.ctx, "dependency", "sqlite.commit",
		"dependency.name", "sqlite",
		"dependency.operation", "commit",
	)
	err := t.Tx.Commit()
	endSpan(err)
	return err
}

func (t tracedTx) Rollback() error {
	_, endSpan := latencytrace.StartSpan(t.ctx, "dependency", "sqlite.rollback",
		"dependency.name", "sqlite",
		"dependency.operation", "rollback",
	)
	err := t.Tx.Rollback()
	endSpan(err)
	return err
}

func sqlOperation(query string) string {
	for _, field := range strings.Fields(query) {
		field = strings.TrimLeft(field, "(`")
		if field == "" || strings.HasPrefix(field, "--") {
			continue
		}
		return strings.ToLower(field)
	}
	return "unknown"
}

func convertNamedValues(args []sqldriver.NamedValue) ([]sqldriver.NamedValue, error) {
	if len(args) == 0 {
		return args, nil
	}
	out := make([]sqldriver.NamedValue, len(args))
	copy(out, args)
	for i := range out {
		if sqldriver.IsValue(out[i].Value) {
			continue
		}
		converted, err := sqldriver.DefaultParameterConverter.ConvertValue(out[i].Value)
		if err != nil {
			return nil, err
		}
		out[i].Value = converted
	}
	return out, nil
}
