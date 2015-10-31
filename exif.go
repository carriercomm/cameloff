package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"camlistore.org/pkg/blobserver/dir"
	"github.com/rwcarlsen/goexif/exif"

	"github.com/dichro/cameloff/db"
	"github.com/dichro/cameloff/fsck"
)

func main() {
	dbDir := flag.String("db_dir", "", "FSCK state database directory")
	blobDir := flag.String("blob_dir", "", "Camlistore blob directory")
	mimeType := flag.String("mime_type", "image/jpeg", "MIME type of files to scan")
	print := flag.Bool("print", false, "Print ref and camera model")
	workers := fsck.Parallel{Workers: 32}
	flag.Var(workers, "workers", "parallel worker goroutines")
	flag.Parse()

	fdb, err := db.New(*dbDir)
	if err != nil {
		log.Fatal(err)
	}
	bs, err := dir.New(*blobDir)
	if err != nil {
		log.Fatal(err)
	}

	stats := fsck.NewStats()
	defer stats.LogTopNEvery(10, 10*time.Second).Stop()
	defer log.Print(stats)

	files := fsck.NewFiles(bs)
	go files.ReadRefs(fdb.ListMIME(*mimeType))
	go files.LogErrors()

	workers.Go(func() {
		for r := range files.Readers {
			ex, err := exif.Decode(r)
			if err != nil {
				stats.Add("error")
				continue
			}
			tag, err := ex.Get(exif.Model)
			if err != nil {
				stats.Add("missing")
				continue
			}
			stats.Add(tag.String())
			if *print {
				fmt.Printf("%s %q %q\n", r.Ref, r.Filename, tag)
			}
		}
	})
	workers.Wait()
}
