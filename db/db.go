package db

import (
	"bytes"
	"fmt"
	"log"
	"strings"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

type DB struct {
	db *leveldb.DB
}

func New(path string) (*DB, error) {
	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		return nil, err
	}
	return &DB{db: db}, nil
}

func NewRO(path string) (*DB, error) {
	db, err := leveldb.OpenFile(path, &opt.Options{
		ErrorIfMissing: true,
		ReadOnly:       true,
	})
	if err != nil {
		return nil, err
	}
	return &DB{db: db}, nil
}

const (
	// prefixes used in leveldb
	found     = "found"
	missing   = "missing"
	parent    = "parent"
	last      = "last"
	camliType = "type"
	mimeType  = "mime"

	// bounds for iterators
	start = "\x00"
	limit = "\xff"
)

func (d *DB) PlaceMIME(ref, mime string) error {
	return d.db.Put(pack(mimeType, mime, ref), nil, nil)
}

// Place notes the presence of a blob at a particular location.
func (d *DB) Place(ref, location, ct string, dependencies []string) (err error) {
	b := new(leveldb.Batch)
	// TODO(dichro): duplicates are interesting, but pretty rare,
	// so probably not worth tracking?
	b.Put(pack(found, ref), pack(location))
	b.Put(pack(last), pack(location))
	if ct != "" {
		b.Put(pack(camliType, ct, ref), nil)
	}
	for _, dep := range dependencies {
		b.Put(pack(parent, dep, ref), nil)
		// TODO(dichro): should these always be looked up
		// inline? Maybe a post-scan would be faster for bulk
		// insert?
		if ok, _ := d.db.Has(pack(found, dep), nil); !ok {
			b.Put(pack(missing, dep, ref), nil)
		}
	}
	it := d.db.NewIterator(&util.Range{
		Start: pack(missing, ref, start),
		Limit: pack(missing, ref, limit),
	}, nil)
	defer it.Release()
	for it.Next() {
		b.Delete(it.Key())
	}
	if err := it.Error(); err != nil {
		fmt.Println(err)
	}
	err = d.db.Write(b, nil)
	return
}

// Last returns the last location successfully Placed.
func (d *DB) Last() string {
	if data, err := d.db.Get(pack(last), nil); err == nil {
		return string(data)
	} else {
		log.Print(err)
	}
	return ""
}

// Missing streams the currently unknown blobs.
func (d *DB) Missing() <-chan string {
	ch := make(chan string)
	go d.streamBlobs(ch, 1, &util.Range{
		Start: pack(missing, start),
		Limit: pack(missing, limit),
	})
	return ch
}

// List streams all known blobs of a particular type.
func (d *DB) List(ct string) <-chan string {
	var rng util.Range
	if ct != "" {
		rng.Start = pack(camliType, ct, start)
		rng.Limit = pack(camliType, ct, limit)
	} else {
		rng.Start = pack(camliType, start)
		rng.Limit = pack(camliType, limit)
	}
	ch := make(chan string)
	go d.streamBlobs(ch, 2, &rng)
	return ch
}

// ListMIME streams all known files of a particular MIME type.
func (d *DB) ListMIME(mt string) <-chan string {
	ch := make(chan string)
	go d.streamBlobs(ch, 2, &util.Range{
		Start: pack(mimeType, mt, start),
		Limit: pack(mimeType, mt, limit),
	})
	return ch
}

func (d *DB) streamBlobs(ch chan<- string, refPos int, rng *util.Range) {
	defer close(ch)
	it := d.db.NewIterator(rng, nil)
	defer it.Release()
	for it.Next() {
		parts := unpack(it.Key())
		ch <- parts[refPos]
	}
}

type Stats struct {
	Blobs, Links, Missing, Unknown uint64
	CamliTypes, MIMETypes          map[string]int64
}

func (s Stats) String() string {
	return fmt.Sprintf("%d blobs, %d links, %d missing; %d unknown index entries",
		s.Blobs, s.Links, s.Missing, s.Unknown)
}

// Stats scans the entire index counting various things.
func (d *DB) Stats() (s Stats) {
	s.CamliTypes = make(map[string]int64)
	s.MIMETypes = make(map[string]int64)
	it := d.db.NewIterator(nil, nil)
	defer it.Release()
	for it.Next() {
		parts := unpack(it.Key())
		switch parts[0] {
		case last:
		case found:
			s.Blobs++
		case parent:
			s.Links++
		case missing:
			s.Missing++
		case camliType:
			s.CamliTypes[parts[1]]++
		case mimeType:
			s.MIMETypes[parts[1]]++
		default:
			s.Unknown++
		}
	}
	return
}

// Parents returns all immediate parents of a blob ref.
func (d *DB) Parents(ref string) (parents []string, err error) {
	it := d.db.NewIterator(&util.Range{
		Start: pack(parent, ref, start),
		Limit: pack(parent, ref, limit),
	}, nil)
	defer it.Release()
	for it.Next() {
		parts := unpack(it.Key())
		parents = append(parents, parts[2])
	}
	err = it.Error()
	return
}

// StreamAllParentPaths resolves and returns all complete parent paths
// for a blob ref.
func (d *DB) StreamAllParentPaths(ref string, ch chan<- []string) error {
	return d.streamAllParentPaths(nil, ref, ch)
}

func (d *DB) streamAllParentPaths(path []string, ref string, ch chan<- []string) error {
	parents, err := d.Parents(ref)
	if err != nil {
		return err
	}
	if len(parents) == 0 {
		ch <- path
		return nil
	}
	for _, parent := range parents {
		if err := d.streamAllParentPaths(append(path, parent), parent, ch); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) Close() {
	if err := d.db.Close(); err != nil {
		log.Print(err)
	}
}

func pack(prefix string, fields ...string) []byte {
	b := bytes.NewBufferString(prefix)
	for _, f := range fields {
		b.WriteByte('|')
		b.WriteString(f)
	}
	return b.Bytes()
}

func unpack(bts []byte) []string {
	return strings.Split(string(bts), "|")
}
