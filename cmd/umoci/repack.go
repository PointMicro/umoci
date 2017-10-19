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

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/apex/log"
	"github.com/openSUSE/umoci/mutate"
	"github.com/openSUSE/umoci/oci/cas/dir"
	"github.com/openSUSE/umoci/oci/casext"
	igen "github.com/openSUSE/umoci/oci/config/generate"
	"github.com/openSUSE/umoci/oci/layer"
	"github.com/openSUSE/umoci/pkg/fseval"
	"github.com/openSUSE/umoci/pkg/mtreefilter"
	ispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	"github.com/vbatts/go-mtree"
	"golang.org/x/net/context"
)

var repackCommand = uxHistory(cli.Command{
	Name:  "repack",
	Usage: "repacks an OCI runtime bundle into a reference",
	ArgsUsage: `--image <image-path>[:<new-tag>] <bundle>

Where "<image-path>" is the path to the OCI image, "<new-tag>" is the name of
the tag that the new image will be saved as (if not specified, defaults to
"latest"), and "<bundle>" is the bundle from which to generate the required
layers.

The "<image-path>" MUST be the same image that was used to create "<bundle>"
(using umoci-unpack(1)). Otherwise umoci will not be able to modify the
original manifest to add the diff layer.

All uid-map and gid-map settings are automatically loaded from the bundle
metadata (which is generated by umoci-unpack(1)) so if you unpacked an image
using a particular mapping then the same mapping will be used to generate the
new layer.

It should be noted that this is not the same as oci-create-layer because it
uses go-mtree to create diff layers from runtime bundles unpacked with
umoci-unpack(1). In addition, it modifies the image so that all of the relevant
manifest and configuration information uses the new diff atop the old manifest.`,

	// repack creates a new image, with a given tag.
	Category: "image",

	Flags: []cli.Flag{
		cli.StringSliceFlag{
			Name:  "mask-path",
			Usage: "set of path prefixes in which deltas will be ignored when generating new layers",
		},
		cli.BoolFlag{
			Name:  "no-mask-volumes",
			Usage: "do not add the Config.Volumes of the image to the set of masked paths",
		},
		cli.BoolFlag{
			Name:  "refresh-bundle",
			Usage: "update the bundle metadata (mtree) to reflect the packed rootfs",
		},
	},

	Action: repack,

	Before: func(ctx *cli.Context) error {
		if ctx.NArg() != 1 {
			return errors.Errorf("invalid number of positional arguments: expected <bundle>")
		}
		if ctx.Args().First() == "" {
			return errors.Errorf("bundle path cannot be empty")
		}
		ctx.App.Metadata["bundle"] = ctx.Args().First()
		return nil
	},
})

func repack(ctx *cli.Context) error {
	imagePath := ctx.App.Metadata["--image-path"].(string)
	tagName := ctx.App.Metadata["--image-tag"].(string)
	bundlePath := ctx.App.Metadata["bundle"].(string)

	// Read the metadata first.
	meta, err := ReadBundleMeta(bundlePath)
	if err != nil {
		return errors.Wrap(err, "read umoci.json metadata")
	}

	log.WithFields(log.Fields{
		"version":     meta.Version,
		"from":        meta.From,
		"map_options": meta.MapOptions,
	}).Debugf("umoci: loaded UmociMeta metadata")

	if meta.From.Descriptor().MediaType != ispec.MediaTypeImageManifest {
		return errors.Wrap(fmt.Errorf("descriptor does not point to ispec.MediaTypeImageManifest: not implemented: %s", meta.From.Descriptor().MediaType), "invalid saved from descriptor")
	}

	// Get a reference to the CAS.
	engine, err := dir.Open(imagePath)
	if err != nil {
		return errors.Wrap(err, "open CAS")
	}
	engineExt := casext.NewEngine(engine)
	defer engine.Close()

	// Create the mutator.
	mutator, err := mutate.New(engine, meta.From)
	if err != nil {
		return errors.Wrap(err, "create mutator for base image")
	}

	mtreeName := strings.Replace(meta.From.Descriptor().Digest.String(), "sha256:", "sha256_", 1)
	mtreePath := filepath.Join(bundlePath, mtreeName+".mtree")
	fullRootfsPath := filepath.Join(bundlePath, layer.RootfsName)

	log.WithFields(log.Fields{
		"image":  imagePath,
		"bundle": bundlePath,
		"rootfs": layer.RootfsName,
		"mtree":  mtreePath,
	}).Debugf("umoci: repacking OCI image")

	mfh, err := os.Open(mtreePath)
	if err != nil {
		return errors.Wrap(err, "open mtree")
	}
	defer mfh.Close()

	spec, err := mtree.ParseSpec(mfh)
	if err != nil {
		return errors.Wrap(err, "parse mtree")
	}

	log.WithFields(log.Fields{
		"keywords": MtreeKeywords,
	}).Debugf("umoci: parsed mtree spec")

	fsEval := fseval.DefaultFsEval
	if meta.MapOptions.Rootless {
		fsEval = fseval.RootlessFsEval
	}

	log.Info("computing filesystem diff ...")
	diffs, err := mtree.Check(fullRootfsPath, spec, MtreeKeywords, fsEval)
	if err != nil {
		return errors.Wrap(err, "check mtree")
	}
	log.Info("... done")

	log.WithFields(log.Fields{
		"ndiff": len(diffs),
	}).Debugf("umoci: checked mtree spec")

	// We need to mask config.Volumes.
	config, err := mutator.Config(context.Background())
	if err != nil {
		return errors.Wrap(err, "get config")
	}
	maskedPaths := ctx.StringSlice("mask-path")
	if !ctx.Bool("no-mask-volumes") {
		for v := range config.Volumes {
			maskedPaths = append(maskedPaths, v)
		}
	}
	diffs = mtreefilter.FilterDeltas(diffs, mtreefilter.MaskFilter(maskedPaths))

	reader, err := layer.GenerateLayer(fullRootfsPath, diffs, &meta.MapOptions)
	if err != nil {
		return errors.Wrap(err, "generate diff layer")
	}
	defer reader.Close()

	imageMeta, err := mutator.Meta(context.Background())
	if err != nil {
		return errors.Wrap(err, "get image metadata")
	}

	created := time.Now()
	history := ispec.History{
		Author:     imageMeta.Author,
		Comment:    "",
		Created:    &created,
		CreatedBy:  "umoci config", // XXX: Should we append argv to this?
		EmptyLayer: false,
	}

	if val, ok := ctx.App.Metadata["--history.author"]; ok {
		history.Author = val.(string)
	}
	if val, ok := ctx.App.Metadata["--history.comment"]; ok {
		history.Comment = val.(string)
	}
	if val, ok := ctx.App.Metadata["--history.created"]; ok {
		created, err := time.Parse(igen.ISO8601, val.(string))
		if err != nil {
			return errors.Wrap(err, "parsing --history.created")
		}
		history.Created = &created
	}
	if val, ok := ctx.App.Metadata["--history.created_by"]; ok {
		history.CreatedBy = val.(string)
	}

	// TODO: We should add a flag to allow for a new layer to be made
	//       non-distributable.
	if err := mutator.Add(context.Background(), reader, history); err != nil {
		return errors.Wrap(err, "add diff layer")
	}

	newDescriptorPath, err := mutator.Commit(context.Background())
	if err != nil {
		return errors.Wrap(err, "commit mutated image")
	}

	log.Infof("new image manifest created: %s->%s", newDescriptorPath.Root().Digest, newDescriptorPath.Descriptor().Digest)

	if err := engineExt.UpdateReference(context.Background(), tagName, newDescriptorPath.Root()); err != nil {
		return errors.Wrap(err, "add new tag")
	}

	log.Infof("created new tag for image manifest: %s", tagName)

	if ctx.Bool("refresh-bundle") {
		newMtreeName := strings.Replace(newDescriptorPath.Descriptor().Digest.String(), "sha256:", "sha256_", 1)
		if err := writeMtree(newMtreeName, bundlePath, fsEval); err != nil {
			return errors.Wrap(err, "write mtree metadata")
		}
		if err := os.Remove(mtreePath); err != nil {
			return errors.Wrap(err, "remove old mtree metadata")
		}
		meta.From = newDescriptorPath
		if err := WriteBundleMeta(bundlePath, meta); err != nil {
			return errors.Wrap(err, "write umoci.json metadata")
		}
	}

	return nil
}
