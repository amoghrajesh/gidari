package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/alpine-hodler/gidari/proto"
)

const (
	// MongoType is the byte representation of a mongo database.
	MongoType uint8 = iota

	// PostgresType is the byte representation of a postgres database.
	PostgresType
)

var (
	// ErrDNSNotSupported is an error that is returned when a DNS is not supported.
	ErrDNSNotSupported = fmt.Errorf("dns is not supported")
)

// DNSNotSupported wraps an error with ErrDNSNotSupported.
func DNSNotSupportedError(dns string) error {
	return fmt.Errorf("%w: %s", ErrDNSNotSupported, dns)
}

// Storage is an interface that defines the methods that a storage device should implement.
type Storage interface {
	// Close will disconnect the storage device.
	Close()

	// ListTables will return a list of all tables in the database.
	ListTables(ctx context.Context) (*proto.ListTablesResponse, error)

	// StartTx will start a transaction and return a "Tx" object that can be used to put operations on a channel,
	// commit the result of all operations sent to the transaction, or rollback the result of all operations sent
	// to the transaction.
	StartTx(context.Context) (Tx, error)

	// Truncate will delete all data from the storage device for ast list of tables.
	Truncate(context.Context, *proto.TruncateRequest) (*proto.TruncateResponse, error)

	// Type returns the type of storage device.
	Type() uint8

	// Upsert will insert or update a batch of records in the storage device.
	Upsert(context.Context, *proto.UpsertRequest) (*proto.UpsertResponse, error)
}

// sqlStmtPreparer can be used to prepare a statement and return the result.
type sqlStmtPreparer interface {
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// Tx is an interface that defines the methods that a transaction object should implement.
type Tx interface {
	Commit() error
	Rollback() error
	Send(TXChanFn)
}

// Scheme takes a byte and returns the associated DNS root database resource.
func Scheme(t uint8) string {
	switch t {
	case MongoType:
		return "mongodb"
	case PostgresType:
		return "postgresql"
	default:
		return "unknown"
	}
}

// New will attempt to return a generic storage object given a DNS.
func New(ctx context.Context, dns string) (Storage, error) {
	if strings.Contains(dns, Scheme(MongoType)) {
		return NewMongo(ctx, dns)
	}

	if strings.Contains(dns, Scheme(PostgresType)) {
		return NewPostgres(ctx, dns)
	}

	return nil, DNSNotSupportedError(dns)
}
