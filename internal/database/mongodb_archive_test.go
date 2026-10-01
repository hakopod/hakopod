package database

import (
	"bytes"
	"encoding/binary"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoDBSnapshotPreservesBSONAndMetadata(t *testing.T) {
	empty, _ := bson.Marshal(bson.D{})
	view, _ := bson.Marshal(bson.D{{Key: "viewOn", Value: "records"}, {Key: "pipeline", Value: bson.A{}}})
	index, _ := bson.Marshal(bson.D{{Key: "name", Value: "value_1"}, {Key: "key", Value: bson.D{{Key: "value", Value: 1}}}, {Key: "unique", Value: true}})
	value, _ := bson.Marshal(bson.D{{Key: "_id", Value: bson.NewObjectID()}, {Key: "data", Value: bson.Binary{Subtype: 128, Data: []byte{0, 10, 255}}}, {Key: "timestamp", Value: bson.Timestamp{T: 42, I: 7}}, {Key: "nested", Value: bson.D{{Key: "items", Value: bson.A{nil, int64(9223372036854775807)}}}}})
	manifest := MongoDBArchive{Version: 1, EngineVersion: "8.0", Collections: []MongoDBCollection{{Name: "records", Type: "collection", Options: empty, Indexes: []bson.Raw{index}}, {Name: "view", Type: "view", Options: view}}}
	var archive bytes.Buffer
	if err := WriteMongoDBArchive(&archive, manifest, func(i int, write func(bson.Raw) error) error {
		if i != 0 {
			t.Fatal("views must not contain copied data")
		}
		return write(value)
	}); err != nil {
		t.Fatal(err)
	}
	collections, documents := 0, 0
	if err := ReadMongoDBArchive(bytes.NewReader(archive.Bytes()), func(c MongoDBCollection) error { collections++; return nil }, func(raw bson.Raw) error {
		documents++
		if !bytes.Equal(raw, value) {
			t.Fatal("BSON changed during snapshot round trip")
		}
		return nil
	}, func(c MongoDBCollection) error {
		if c.Name == "records" && !bytes.Equal(c.Indexes[0], index) {
			t.Fatal("index metadata changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if collections != 2 || documents != 1 {
		t.Fatal("snapshot cardinality changed")
	}
	noop := func(MongoDBCollection) error { return nil }
	for n := 0; n < archive.Len(); n++ {
		if ReadMongoDBArchive(bytes.NewReader(archive.Bytes()[:n]), noop, func(bson.Raw) error { return nil }, noop) == nil {
			t.Fatal("truncated snapshot accepted", n)
		}
	}
	if ReadMongoDBArchive(bytes.NewReader(append(archive.Bytes(), 1)), noop, func(bson.Raw) error { return nil }, noop) == nil {
		t.Fatal("snapshot trailer accepted")
	}
}

func TestMongoDBSnapshotRejectsUnboundedAndSystemMetadata(t *testing.T) {
	empty, _ := bson.Marshal(bson.D{})
	for _, name := range []string{"", "system.users", "bad\x00name", "$cmd"} {
		manifest := MongoDBArchive{Version: 1, EngineVersion: "8.0", Collections: []MongoDBCollection{{Name: name, Type: "collection", Options: empty}}}
		if manifest.Validate() == nil {
			t.Fatal("unsafe collection accepted")
		}
	}
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], 0xffffffff)
	if _, err := readMongoDBFrame(bytes.NewReader(size[:]), MongoDBMaxDocumentBytes); err == nil {
		t.Fatal("unbounded BSON allocation accepted")
	}
}
