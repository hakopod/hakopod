package database

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const MongoDBAgentCPU = "100m"
const MongoDBAgentMemory = "256Mi"
const MongoDBLogStorageGiB int64 = 1
const MongoDBMaxCollections = 256
const MongoDBMaxDocumentBytes = 16 << 20
const mongoArchiveMagic = "HAKOPOD-MONGODB-1\n"

// Collection options and indexes retain BSON types. Documents are framed raw
// BSON rather than JSON, preserving binary subtypes, decimals and timestamps.
type MongoDBCollection struct {
	Name    string     `json:"name"`
	Type    string     `json:"type"`
	UUID    []byte     `json:"uuid,omitempty"`
	Options bson.Raw   `json:"options"`
	Indexes []bson.Raw `json:"indexes"`
}

type MongoDBArchive struct {
	Version       int                 `json:"version"`
	EngineVersion string              `json:"engine_version"`
	Collections   []MongoDBCollection `json:"collections"`
}

func (a MongoDBArchive) Validate() error {
	if a.Version != 1 || a.EngineVersion != "8.0" || len(a.Collections) > MongoDBMaxCollections {
		return fmt.Errorf("unsupported MongoDB snapshot manifest")
	}
	seen := map[string]bool{}
	for _, c := range a.Collections {
		if !validMongoDBCollectionName(c.Name) || seen[c.Name] {
			return fmt.Errorf("invalid MongoDB snapshot collection")
		}
		seen[c.Name] = true
		if c.Type != "collection" && c.Type != "view" && c.Type != "timeseries" {
			return fmt.Errorf("unsupported MongoDB collection type")
		}
		if len(c.Options) > 1<<20 || c.Options.Validate() != nil || len(c.Indexes) > 64 {
			return fmt.Errorf("invalid MongoDB collection metadata")
		}
		if _, err := mongoDBCollectionOptions(c); err != nil {
			return err
		}
		for _, index := range c.Indexes {
			if len(index) > 1<<20 || index.Validate() != nil {
				return fmt.Errorf("invalid MongoDB index metadata")
			}
			if _, err := mongoDBIndexOptions(index); err != nil {
				return err
			}
		}
		if c.Type == "view" && len(c.Indexes) != 0 {
			return fmt.Errorf("MongoDB views cannot have indexes")
		}
	}
	return nil
}

func WriteMongoDBArchive(out io.Writer, manifest MongoDBArchive, capture func(int, func(bson.Raw) error) error) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	header, err := json.Marshal(manifest)
	if err != nil || len(header) > 8<<20 {
		return fmt.Errorf("MongoDB snapshot metadata exceeds its bound")
	}
	if _, err = io.WriteString(out, mongoArchiveMagic); err != nil {
		return err
	}
	if err = writeMongoDBFrame(out, header); err != nil {
		return err
	}
	for i, collection := range manifest.Collections {
		if collection.Type != "view" {
			if err = capture(i, func(raw bson.Raw) error {
				if len(raw) > MongoDBMaxDocumentBytes || raw.Validate() != nil {
					return fmt.Errorf("invalid MongoDB snapshot document")
				}
				return writeMongoDBFrame(out, raw)
			}); err != nil {
				return err
			}
		}
		if err = writeMongoDBFrame(out, nil); err != nil {
			return err
		}
	}
	return nil
}

func writeMongoDBFrame(out io.Writer, data []byte) error {
	var length [4]byte
	binary.LittleEndian.PutUint32(length[:], uint32(len(data)))
	if n, err := out.Write(length[:]); err != nil {
		return err
	} else if n != len(length) {
		return io.ErrShortWrite
	}
	n, err := out.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func readMongoDBFrame(in io.Reader, maximum uint32) ([]byte, error) {
	var length [4]byte
	if _, err := io.ReadFull(in, length[:]); err != nil {
		return nil, fmt.Errorf("truncated MongoDB snapshot")
	}
	n := binary.LittleEndian.Uint32(length[:])
	if n > maximum {
		return nil, fmt.Errorf("MongoDB snapshot frame exceeds its bound")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(in, data); err != nil {
		return nil, fmt.Errorf("truncated MongoDB snapshot frame")
	}
	return data, nil
}

// ReadMongoDBArchive invokes only application-collection callbacks. It never
// supplies a database name or an executable command from archive input.
func ReadMongoDBArchive(in io.Reader, begin func(MongoDBCollection) error, insert func(bson.Raw) error, finish func(MongoDBCollection) error) error {
	magic := make([]byte, len(mongoArchiveMagic))
	if _, err := io.ReadFull(in, magic); err != nil || string(magic) != mongoArchiveMagic {
		return fmt.Errorf("invalid MongoDB snapshot header")
	}
	header, err := readMongoDBFrame(in, 8<<20)
	if err != nil {
		return err
	}
	var manifest MongoDBArchive
	decoder := json.NewDecoder(bytes.NewReader(header))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || manifest.Validate() != nil {
		return fmt.Errorf("invalid MongoDB snapshot manifest")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid MongoDB snapshot manifest trailer")
	}
	for _, collection := range manifest.Collections {
		if err = begin(collection); err != nil {
			return err
		}
		for {
			raw, err := readMongoDBFrame(in, MongoDBMaxDocumentBytes)
			if err != nil {
				return err
			}
			if len(raw) == 0 {
				break
			}
			if collection.Type == "view" || bson.Raw(raw).Validate() != nil {
				return fmt.Errorf("invalid MongoDB snapshot document")
			}
			if err = insert(raw); err != nil {
				return err
			}
		}
		if err = finish(collection); err != nil {
			return err
		}
	}
	var extra [1]byte
	if n, err := in.Read(extra[:]); n != 0 || err != io.EOF {
		return fmt.Errorf("unexpected MongoDB snapshot trailer")
	}
	return nil
}
