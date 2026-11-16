package query

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

func TestBandwidthRejectsUnknownSplit(t *testing.T) {
	_, err := NewReader(nil).Bandwidth(context.Background(), BandwidthRequest{Split: "status"})
	if !errors.Is(err, ErrUnknownBandwidthSplit) {
		t.Fatalf("error %v, want ErrUnknownBandwidthSplit", err)
	}
}

const testRestoreID = "01890a5d-ac96-774b-bcce-b302099a8057"

type emptyRows struct {
	driver.Rows
}

func (emptyRows) Next() bool   { return false }
func (emptyRows) Err() error   { return nil }
func (emptyRows) Close() error { return nil }

type capturingConnection struct {
	driver.Conn
	statement string
	args      []any
}

func (connection *capturingConnection) Query(ctx context.Context, statement string, args ...any) (driver.Rows, error) {
	connection.statement = statement
	connection.args = args
	return emptyRows{}, nil
}

func (connection *capturingConnection) QueryFormat(ctx context.Context, format string, statement string, args ...any) (io.ReadCloser, error) {
	connection.statement = statement
	connection.args = args
	return io.NopCloser(strings.NewReader("")), nil
}

func readerCalls(restoreID string) map[string]func(*Reader) error {
	rangeFrom := time.Unix(1700000000, 0)
	rangeTo := rangeFrom.Add(time.Hour)
	return map[string]func(*Reader) error{
		"search": func(reader *Reader) error {
			_, err := reader.Search(context.Background(), SearchRequest{Filter: Filter{Where: "1"}, From: rangeFrom, To: rangeTo, Limit: 10, RestoreID: restoreID})
			return err
		},
		"detail": func(reader *Reader) error {
			_, err := reader.Detail(context.Background(), RequestKey{Project: "shop", Domain: "example.com", Timestamp: rangeFrom, RequestID: "request-1", RestoreID: restoreID})
			if errors.Is(err, ErrRequestNotFound) {
				return nil
			}
			return err
		},
		"tape": func(reader *Reader) error {
			_, err := reader.Tape(context.Background(), TapeRequest{Filter: Filter{Where: "1"}, From: rangeFrom, To: rangeTo, StepSeconds: 60, RestoreID: restoreID})
			return err
		},
		"facets": func(reader *Reader) error {
			_, err := reader.Facets(context.Background(), FacetRequest{Field: "domain", From: rangeFrom, To: rangeTo, RestoreID: restoreID})
			return err
		},
		"export": func(reader *Reader) error {
			stream, err := reader.Export(context.Background(), ExportRequest{Filter: Filter{Where: "1"}, From: rangeFrom, To: rangeTo, RestoreID: restoreID}, "JSONEachRow")
			if err == nil {
				stream.Close()
			}
			return err
		},
	}
}

func TestRestoreIDIsBoundAsParameter(t *testing.T) {
	for name, call := range readerCalls(testRestoreID) {
		t.Run(name, func(t *testing.T) {
			connection := &capturingConnection{}
			if err := call(NewReader(connection)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(connection.statement, "FROM requests_restored") || !strings.Contains(connection.statement, "restore_id = {restore_id:String}") {
				t.Fatalf("statement does not read the restored table through a bound parameter: %s", connection.statement)
			}
			if strings.Contains(connection.statement, testRestoreID) {
				t.Fatalf("statement contains the restore id: %s", connection.statement)
			}
			found := false
			for _, arg := range connection.args {
				if reflect.DeepEqual(arg, clickhouse.Named("restore_id", testRestoreID)) {
					found = true
				}
			}
			if !found {
				t.Fatalf("args %v do not bind restore_id", connection.args)
			}
		})
	}
}

func TestLiveSourceReadsRequestsTable(t *testing.T) {
	for name, call := range readerCalls("") {
		t.Run(name, func(t *testing.T) {
			connection := &capturingConnection{}
			if err := call(NewReader(connection)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(connection.statement, "FROM requests\n") || strings.Contains(connection.statement, "requests_restored") {
				t.Fatalf("statement does not read the live table: %s", connection.statement)
			}
			for _, arg := range connection.args {
				if named, ok := arg.(driver.NamedValue); ok && named.Name == "restore_id" {
					t.Fatalf("live statement binds restore_id: %v", connection.args)
				}
			}
		})
	}
}
