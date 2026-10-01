package database

import (
	"bytes"
	"io"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoDBRecoveryRejectsCommandAndUnsupportedSchemaOptions(t *testing.T) {
	for _, option := range []bson.D{
		{{Key: "create", Value: "other"}},
		{{Key: "$db", Value: "admin"}},
		{{Key: "writeConcern", Value: bson.D{{Key: "w", Value: 0}}}},
		{{Key: "validator", Value: bson.D{}}, {Key: "validator", Value: bson.D{}}},
		{{Key: "encryptedFields", Value: bson.D{}}},
	} {
		raw, err := bson.Marshal(option)
		if err != nil {
			t.Fatal(err)
		}
		manifest := MongoDBArchive{Version: 1, EngineVersion: "8.0", Collections: []MongoDBCollection{{Name: "records", Type: "collection", Options: raw}}}
		if manifest.Validate() == nil {
			t.Fatal("unsupported or command-bearing collection option was accepted")
		}
	}
	for _, source := range []string{"system.users", "$cmd", ""} {
		raw, _ := bson.Marshal(bson.D{{Key: "viewOn", Value: source}, {Key: "pipeline", Value: bson.A{}}})
		if _, err := mongoDBCollectionOptions(MongoDBCollection{Type: "view", Options: raw}); err == nil {
			t.Fatal("unsafe view source accepted")
		}
	}
	index, _ := bson.Marshal(bson.D{{Key: "name", Value: "valid"}, {Key: "key", Value: bson.D{{Key: "value", Value: 1}}}, {Key: "dropDatabase", Value: 1}})
	if _, err := mongoDBIndexOptions(index); err == nil {
		t.Fatal("command-bearing index metadata accepted")
	}
}

func TestMongoDBRecoveryPreservesIndexSemantics(t *testing.T) {
	index, _ := bson.Marshal(bson.D{{Key: "v", Value: 2}, {Key: "name", Value: "value_unique"}, {Key: "key", Value: bson.D{{Key: "value", Value: 1}}}, {Key: "unique", Value: true}, {Key: "partialFilterExpression", Value: bson.D{{Key: "active", Value: true}}}, {Key: "hidden", Value: true}})
	options, err := mongoDBIndexOptions(index)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := bson.Marshal(options)
	if bson.Raw(raw).Lookup("v").Type != 0 || !bson.Raw(raw).Lookup("unique").Boolean() || !bson.Raw(raw).Lookup("hidden").Boolean() || bson.Raw(raw).Lookup("partialFilterExpression").Type != bson.TypeEmbeddedDocument {
		t.Fatal("index semantics were lost")
	}
}

type mongoDBShortWriter struct{}

func (mongoDBShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestMongoDBArchiveRejectsShortWritesAndHeaderExtensions(t *testing.T) {
	if err := writeMongoDBFrame(mongoDBShortWriter{}, []byte{1}); err != io.ErrShortWrite {
		t.Fatal("short archive write was not reported")
	}
	for _, header := range []string{
		`{"version":1,"engine_version":"8.0","collections":[],"command":"dropDatabase"}`,
		`{"version":1,"engine_version":"8.0","collections":[]} {}`,
	} {
		var archive bytes.Buffer
		archive.WriteString(mongoArchiveMagic)
		if err := writeMongoDBFrame(&archive, []byte(header)); err != nil {
			t.Fatal(err)
		}
		called := false
		noop := func(MongoDBCollection) error { called = true; return nil }
		if err := ReadMongoDBArchive(&archive, noop, func(bson.Raw) error { called = true; return nil }, noop); err == nil || called {
			t.Fatal("unrecognized header reached restore callbacks")
		}
	}
}
