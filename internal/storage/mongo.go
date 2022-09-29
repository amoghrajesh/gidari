package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/alpine-hodler/gidari/proto"
	"github.com/alpine-hodler/gidari/tools"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
)

const defaultMDBLifetime = 60 * time.Second

// Mongo is a wrapper for *mongo.Client, use to perform CRUD operations on a mongo DB instance.
type Mongo struct {
	*mongo.Client
	dns      string
	lifetime time.Duration
}

// NewMongo will return a new mongo client that can be used to perform CRUD operations on a mongo DB instance. This
// constructor uses a URI to make the client connection, and the URI is of the form
// Mongo://username:password@host:port
func NewMongo(ctx context.Context, uri string) (*Mongo, error) {
	clientOptions := options.Client().ApplyURI(uri)

	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		return nil, fmt.Errorf("error connecting to mongo: %w", err)
	}

	mdb := new(Mongo)
	mdb.Client = client
	mdb.dns = uri
	mdb.lifetime = defaultMDBLifetime

	return mdb, nil
}

// IsNoSQL returns "true" indicating that the "MongoDB" database is NoSQL.
func (m *Mongo) IsNoSQL() bool { return true }

// Type returns the type of storage.
func (m *Mongo) Type() uint8 {
	return MongoType
}

// Close will close the mongo client.
func (m *Mongo) Close() {
	if err := m.Client.Disconnect(context.Background()); err != nil {
		panic(err)
	}
}

// ReceiveWrites will listen for writes to the transaction and commit them to the database every time the lifetime
// limit is reached, or when the transaction is committed through the commit channel.
func (m *Mongo) receiveWrites(ctx mongo.SessionContext, txn *Txn) error {
	lifetimeTicker := time.NewTicker(m.lifetime)

	var err error

	// Receive write requests.
	for opr := range txn.ch {
		select {
		case <-lifetimeTicker.C:
			// If the transaction has exceeded the lifetime, commit the transaction and start a new
			// one.
			if err := ctx.CommitTransaction(ctx); err != nil {
				panic(fmt.Errorf("commit transaction: %w", err))
			}

			// Start a new transaction on the context.
			if err := ctx.StartTransaction(); err != nil {
				panic(fmt.Errorf("error starting transaction: %w", err))
			}
		default:
			if err != nil {
				continue
			}

			err = opr(ctx, m)
		}
	}

	if err != nil {
		return fmt.Errorf("error in transaction: %w", err)
	}

	return nil
}

// startSession will create a session and listen for writes, committing and reseting the transaction every 60 seconds
// to avoid lifetime limit errors.
func (m *Mongo) startSession(ctx context.Context, txn *Txn) {
	txn.done <- m.Client.UseSession(ctx, func(sctx mongo.SessionContext) error {
		// Start the transaction, if there is an error break the go routine.
		err := sctx.StartTransaction()
		if err != nil {
			return fmt.Errorf("error starting transaction: %w", err)
		}

		if err := m.receiveWrites(sctx, txn); err != nil {
			return fmt.Errorf("error receiving writes: %w", err)
		}

		// Await the decision to commit or rollback.
		switch {
		case <-txn.commit:
			if err := sctx.CommitTransaction(sctx); err != nil {
				return fmt.Errorf("commit transaction: %w", err)
			}
		default:
			if err := sctx.AbortTransaction(sctx); err != nil {
				return ErrTransactionAborted
			}
		}

		return nil
	})
}

// StartTx will start a mongodb session where all data from write methods can be rolled back.
//
// MongoDB best practice is to "abort any multi-document transactions that runs for more than 60 seconds". The resulting
// error for exceeding this time constraint is "TransactionExceededLifetimeLimitSeconds". To maintain agnostism at the
// repository layer, we implement the logic to handle these transactions errors in the storage layer. Therefore, every
// 60 seconds, the transacting data will be committed commit the transaction and start a new one.
func (m *Mongo) StartTx(ctx context.Context) (*Txn, error) {
	// Construct a transaction.
	txn := &Txn{
		make(chan TxnChanFn),
		make(chan error, 1),
		make(chan bool, 1),
	}

	// Create a go routine that creates a session and listens for writes.
	go m.startSession(ctx, txn)

	return txn, nil
}

// Truncate will delete all records in a collection.
func (m *Mongo) Truncate(ctx context.Context, req *proto.TruncateRequest) (*proto.TruncateResponse, error) {
	// If there are no collections to truncate, return.
	if len(req.Tables) == 0 {
		return &proto.TruncateResponse{}, nil
	}

	connString, err := connstring.ParseAndValidate(m.dns)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connstring: %w", err)
	}

	for _, collection := range req.GetTables() {
		coll := m.Client.Database(connString.Database).Collection(collection)

		_, err = coll.DeleteMany(ctx, bson.M{})
		if err != nil {
			return nil, fmt.Errorf("error truncating collection %s: %w", collection, err)
		}
	}

	return &proto.TruncateResponse{}, nil
}

// Upsert will insert or update a record in a collection.
func (m *Mongo) Upsert(ctx context.Context, req *proto.UpsertRequest) (*proto.UpsertResponse, error) {
	records, err := tools.DecodeUpsertRecords(req)
	if err != nil {
		return nil, fmt.Errorf("failed to decode records: %w", err)
	}

	// If there are no records to upsert, return.
	if len(records) == 0 {
		return &proto.UpsertResponse{}, nil
	}

	models := []mongo.WriteModel{}

	for _, record := range records {
		doc := bson.D{}
		if err := tools.AssingRecordBSONDocument(record, &doc); err != nil {
			return nil, fmt.Errorf("failed to assign record to bson document: %w", err)
		}

		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(doc).
			SetUpdate(bson.D{primitive.E{Key: "$set", Value: doc}}).
			SetUpsert(true))
	}

	cs, err := connstring.ParseAndValidate(m.dns)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	coll := m.Client.Database(cs.Database).Collection(req.Table)

	bwr, err := coll.BulkWrite(ctx, models)
	if err != nil {
		return nil, fmt.Errorf("bulk write error: %w", err)
	}

	rsp := &proto.UpsertResponse{
		MatchedCount:  bwr.MatchedCount,
		UpsertedCount: bwr.UpsertedCount,
	}

	return rsp, nil
}

// ListPrimaryKeys will return a "proto.ListPrimaryKeysResponse" containing a list of primary keys data for all tables
// in a database. MongoDB does not have a concept of primary keys, so we will return the "_id" field as the primary key
// for all collections in the database associated with the underlying connection string.
func (m *Mongo) ListPrimaryKeys(ctx context.Context) (*proto.ListPrimaryKeysResponse, error) {
	collections, err := m.ListTables(ctx)
	if err != nil {
		return nil, fmt.Errorf("error listing collections: %w", err)
	}

	rsp := &proto.ListPrimaryKeysResponse{PKSet: make(map[string]*proto.PrimaryKeys)}

	for collection := range collections.GetTableSet() {
		if rsp.PKSet[collection] == nil {
			rsp.PKSet[collection] = &proto.PrimaryKeys{}
		}

		rsp.PKSet[collection].List = append(rsp.PKSet[collection].List, "_id")
	}

	return rsp, nil
}

// ListTables will return a list of all tables in the MongoDB database.
func (m *Mongo) ListTables(ctx context.Context) (*proto.ListTablesResponse, error) {
	cs, err := connstring.ParseAndValidate(m.dns)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	collections, err := m.Client.Database(cs.Database).ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("failed to list collections: %w", err)
	}

	rsp := &proto.ListTablesResponse{TableSet: make(map[string]bool)}

	for _, collection := range collections {
		rsp.TableSet[collection] = true
	}

	return rsp, nil
}
