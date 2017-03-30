/*
 * umoci: Umoci Modifies Open Containers' Images
 * Copyright (C) 2016, 2017 SUSE LLC.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package cas

import (
	"fmt"
	"io"

	// We need to include sha256 in order for go-digest to properly handle such
	// hashes, since Go's crypto library like to lazy-load cryptographic
	// libraries.
	_ "crypto/sha256"

	"github.com/opencontainers/go-digest"
	ispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/net/context"
)

const (
	// BlobAlgorithm is the name of the only supported digest algorithm for blobs.
	// FIXME: We can make this a list.
	BlobAlgorithm = digest.SHA256
)

// Exposed errors.
var (
	// ErrInvalid is returned when an image was detected as being invalid.
	ErrInvalid = fmt.Errorf("invalid image detected")

	// ErrUnknownType is returned when a mediatype was encountered that is not
	// known how to parse. According to the spec, any caller of a function that
	// can return ErrUnknownType must ignore the error if it is returned.
	ErrUnknownType = fmt.Errorf("unknown mediatype encountered")

	// ErrNotImplemented is returned when a requested operation has not been
	// implementing the backing image store.
	ErrNotImplemented = fmt.Errorf("operation not implemented")

	// ErrClobber is returned when a requested operation would require clobbering a
	// reference or blob which already exists.
	ErrClobber = fmt.Errorf("operation would clobber existing object")
)

// Engine is an interface that provides methods for accessing and modifying an
// OCI image, namely allowing access to reference descriptors and blobs.
type Engine interface {
	// PutBlob adds a new blob to the image. This is idempotent; a nil error
	// means that "the content is stored at DIGEST" without implying "because
	// of this PutBlob() call".
	PutBlob(ctx context.Context, reader io.Reader) (digest digest.Digest, size int64, err error)

	// PutBlobJSON adds a new JSON blob to the image (marshalled from the given
	// interface). This is equivalent to calling PutBlob() with a JSON payload
	// as the reader. Note that due to intricacies in the Go JSON
	// implementation, we cannot guarantee that two calls to PutBlobJSON() will
	// return the same digest.
	//
	// TODO: Use a proper JSON serialisation library, which actually guarantees
	//       consistent output. Go's JSON library doesn't even attempt to sort
	//       map[...]... objects (which have their iteration order randomised
	//       in Go).
	PutBlobJSON(ctx context.Context, data interface{}) (digest digest.Digest, size int64, err error)

	// PutReference adds a new reference descriptor blob to the image. This is
	// idempotent; a nil error means that "the descriptor is stored at NAME"
	// without implying "because of this PutReference() call". ErrClobber is
	// returned if there is already a descriptor stored at NAME, but does not
	// match the descriptor requested to be stored.
	PutReference(ctx context.Context, name string, descriptor ispec.Descriptor) (err error)

	// GetBlob returns a reader for retrieving a blob from the image, which the
	// caller must Close(). Returns os.ErrNotExist if the digest is not found.
	GetBlob(ctx context.Context, digest digest.Digest) (reader io.ReadCloser, err error)

	// GetReference returns a reference from the image. Returns os.ErrNotExist
	// if the name was not found.
	GetReference(ctx context.Context, name string) (descriptor ispec.Descriptor, err error)

	// DeleteBlob removes a blob from the image. This is idempotent; a nil
	// error means "the content is not in the store" without implying "because
	// of this DeleteBlob() call".
	DeleteBlob(ctx context.Context, digest digest.Digest) (err error)

	// DeleteReference removes a reference from the image. This is idempotent;
	// a nil error means "the content is not in the store" without implying
	// "because of this DeleteReference() call".
	DeleteReference(ctx context.Context, name string) (err error)

	// ListBlobs returns the set of blob digests stored in the image.
	ListBlobs(ctx context.Context) (digests []digest.Digest, err error)

	// ListReferences returns the set of reference names stored in the image.
	ListReferences(ctx context.Context) (names []string, err error)

	// Clean executes a garbage collection of any non-blob garbage in the store
	// (this includes temporary files and directories not reachable from the
	// CAS interface). This MUST NOT remove any blobs or references in the
	// store.
	Clean(ctx context.Context) (err error)

	// Close releases all references held by the engine. Subsequent operations
	// may fail.
	Close() (err error)
}
