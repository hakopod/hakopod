package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
)

const oracleArchiveMagic = "HAKOPOD_ORACLE_DATAPUMP_V1\n"
const OracleArchiveMaxBytes int64 = 32 << 30

type OracleArchive struct {
	SchemaVersion int    `json:"schema_version"`
	DatabaseID    string `json:"database_id"`
	Revision      int64  `json:"revision"`
	Version       string `json:"version"`
	Edition       string `json:"edition"`
	SCN           uint64 `json:"scn"`
	Bytes         int64  `json:"bytes"`
}

func (a OracleArchive) validate() error {
	compatible := a.Edition == "free" && a.Version == "23.26" || a.Edition == "enterprise" && (a.Version == "19" || a.Version == "23.26")
	if a.SchemaVersion != 1 || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(a.DatabaseID) || a.Revision < 1 || !compatible || a.SCN == 0 || a.Bytes < 1 || a.Bytes > OracleArchiveMaxBytes {
		return fmt.Errorf("Oracle archive identity or bounds are invalid")
	}
	return nil
}

type oracleArchiveWriter struct {
	out       io.Writer
	remaining int64
}

func (w *oracleArchiveWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("Oracle dump exceeded its declared size")
	}
	n, err := w.out.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func WriteOracleArchive(out io.Writer, a OracleArchive, dump func(io.Writer) error) error {
	if err := a.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if _, err = io.WriteString(out, oracleArchiveMagic); err != nil {
		return err
	}
	if err = binary.Write(out, binary.BigEndian, uint32(len(raw))); err != nil {
		return err
	}
	if _, err = out.Write(raw); err != nil {
		return err
	}
	hash := sha256.New()
	writer := &oracleArchiveWriter{out: io.MultiWriter(out, hash), remaining: a.Bytes}
	if err = dump(writer); err != nil {
		return err
	}
	if writer.remaining != 0 {
		return fmt.Errorf("Oracle dump was truncated")
	}
	_, err = out.Write(hash.Sum(nil))
	return err
}

// The callback stages bytes only. Database writes must wait until the complete
// archive, digest and authenticated outer stream have passed validation.
func ReadOracleArchive(in io.Reader, stage func(OracleArchive, io.Reader) error) (OracleArchive, error) {
	var a OracleArchive
	magic := make([]byte, len(oracleArchiveMagic))
	if _, err := io.ReadFull(in, magic); err != nil || string(magic) != oracleArchiveMagic {
		return a, fmt.Errorf("Oracle archive header is invalid")
	}
	var size uint32
	if err := binary.Read(in, binary.BigEndian, &size); err != nil || size < 1 || size > 4096 {
		return a, fmt.Errorf("Oracle archive manifest exceeds its bound")
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(in, raw); err != nil {
		return a, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&a) != nil {
		return a, fmt.Errorf("Oracle archive manifest is invalid")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return a, fmt.Errorf("Oracle archive manifest contains trailing data")
	}
	if err := a.validate(); err != nil {
		return a, err
	}
	limited := &io.LimitedReader{R: in, N: a.Bytes}
	hash := sha256.New()
	if err := stage(a, io.TeeReader(limited, hash)); err != nil {
		return a, err
	}
	if limited.N != 0 {
		return a, fmt.Errorf("Oracle dump staging was incomplete")
	}
	digest := make([]byte, sha256.Size)
	if _, err := io.ReadFull(in, digest); err != nil || !bytes.Equal(hash.Sum(nil), digest) {
		return a, fmt.Errorf("Oracle dump integrity check failed")
	}
	var end [1]byte
	if n, err := in.Read(end[:]); n != 0 || err != io.EOF {
		return a, fmt.Errorf("Oracle archive contains trailing data")
	}
	return a, nil
}
