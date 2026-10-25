package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/october-dev/agent-intercom/bus"
)

func attachmentCommand(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("use attachment list/upload/download/delete")
	}
	flags := flag.NewFlagSet("attachment "+args[0], flag.ContinueOnError)
	address := flags.String("address", "", "Bus address")
	id := flags.String("id", "", "attachment ID")
	file := flags.String("file", "", "upload path or - for stdin; download destination must be new")
	name := flags.String("name", "", "display filename; required with stdin")
	media := flags.String("type", "", "media type, for example application/pdf")
	key := flags.String("key", "", "idempotency key for upload retries")
	after := flags.String("after", "", "list pagination cursor")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("attachment commands accept flags only")
	}
	client, err := executionClient(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var result any
	switch args[0] {
	case "list":
		result, err = client.Attachments(ctx, *after, 50)
	case "upload":
		if *file == "" {
			return errors.New("upload requires --file <path|->")
		}
		if *file != "-" {
			f, err := os.Open(*file)
			if err != nil {
				return err
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("upload path must be a regular file")
			}
			in = f
			if *name == "" {
				*name = filepath.Base(*file)
			}
		}
		if *media == "" {
			*media, _, _ = mime.ParseMediaType(mime.TypeByExtension(filepath.Ext(*name)))
		}
		data, err := io.ReadAll(io.LimitReader(in, bus.MaxAttachmentBytes+1))
		if err != nil {
			return err
		}
		if len(data) > bus.MaxAttachmentBytes {
			return errors.New("attachment exceeds 16 MiB")
		}
		result, err = client.UploadAttachment(ctx, bus.UploadAttachmentInput{Name: *name, MediaType: *media, IdempotencyKey: *key}, data)
	case "download":
		if *file == "" || *file == "-" {
			return errors.New("download requires --file <new-path>; bytes are never printed to the terminal")
		}
		// Create exclusively before downloading, so an existing file is never lost.
		f, err := os.OpenFile(*file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		keep := false
		defer func() {
			f.Close()
			if !keep {
				os.Remove(*file)
			}
		}()
		var offset int64
		var metadata bus.Attachment
		hash := sha256.New()
		for {
			chunk, err := client.ReadAttachment(ctx, *id, offset, bus.MaxAttachmentChunkBytes)
			if err != nil {
				return err
			}
			if chunk.Attachment.ID != *id || chunk.Offset != offset || (offset > 0 && (metadata.SHA256 != chunk.Attachment.SHA256 || metadata.Size != chunk.Attachment.Size)) {
				return errors.New("download metadata changed")
			}
			metadata = chunk.Attachment
			if _, err := io.MultiWriter(f, hash).Write(chunk.Data); err != nil {
				return err
			}
			offset += int64(len(chunk.Data))
			if chunk.NextOffset == nil {
				break
			}
			if *chunk.NextOffset != offset || len(chunk.Data) == 0 {
				return errors.New("invalid download continuation")
			}
		}
		if offset != metadata.Size || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), metadata.SHA256) {
			return errors.New("download checksum mismatch")
		}
		if err := f.Sync(); err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		keep = true
		result = metadata
	case "delete":
		err = client.DeleteAttachment(ctx, *id)
		result = map[string]bool{"deleted": true}
	default:
		return errors.New("use attachment list/upload/download/delete")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}
