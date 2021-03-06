package blobs

import (
	"bytes"
	"context"
	"io"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/moby/buildkit/cache"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// CompressionType represents compression type for blob data.
type CompressionType int

const (
	// Uncompressed indicates no compression.
	Uncompressed CompressionType = iota

	// Gzip is used for blob data.
	Gzip

	// UnknownCompression means not supported yet.
	UnknownCompression CompressionType = -1
)

var DefaultCompression = Gzip

func (ct CompressionType) String() string {
	switch ct {
	case Uncompressed:
		return "uncompressed"
	case Gzip:
		return "gzip"
	default:
		return "unknown"
	}
}

// DetectCompressionType returns media type from existing blob data.
func DetectLayerMediaType(ctx context.Context, cs content.Store, id digest.Digest, oci bool) (string, error) {
	ra, err := cs.ReaderAt(ctx, ocispec.Descriptor{Digest: id})
	if err != nil {
		return "", err
	}
	defer ra.Close()

	ct, err := detectCompressionType(content.NewReader(ra))
	if err != nil {
		return "", err
	}

	switch ct {
	case Uncompressed:
		if oci {
			return ocispec.MediaTypeImageLayer, nil
		}
		return images.MediaTypeDockerSchema2Layer, nil
	case Gzip:
		if oci {
			return ocispec.MediaTypeImageLayerGzip, nil
		}
		return images.MediaTypeDockerSchema2LayerGzip, nil
	default:
		return "", errors.Errorf("failed to detect layer %v compression type", id)
	}
}

// detectCompressionType detects compression type from real blob data.
func detectCompressionType(cr io.Reader) (CompressionType, error) {
	var buf [10]byte
	var n int
	var err error

	if n, err = cr.Read(buf[:]); err != nil && err != io.EOF {
		// Note: we'll ignore any io.EOF error because there are some
		// odd cases where the layer.tar file will be empty (zero bytes)
		// and we'll just treat it as a non-compressed stream and that
		// means just create an empty layer.
		//
		// See issue docker/docker#18170
		return UnknownCompression, err
	}

	for c, m := range map[CompressionType][]byte{
		Gzip: {0x1F, 0x8B, 0x08},
	} {
		if n < len(m) {
			continue
		}
		if bytes.Equal(m, buf[:len(m)]) {
			return c, nil
		}
	}
	return Uncompressed, nil
}

// GetMediaTypeForLayers retrieves media type for layer from ref information.
// If there is a mismatch in diff IDs or blobsums between the diffPairs and
// corresponding ref, the returned slice will have an empty media type for
// that layer and all parents.
func GetMediaTypeForLayers(diffPairs []DiffPair, ref cache.ImmutableRef) []string {
	layerTypes := make([]string, len(diffPairs))
	if ref == nil {
		return layerTypes
	}

	tref := ref.Clone()
	// diffPairs is ordered parent->child, but we iterate over refs from child->parent,
	// so iterate over diffPairs in reverse
	for i := range diffPairs {
		dp := diffPairs[len(diffPairs)-1-i]

		info := tref.Info()
		if !(info.DiffID == dp.DiffID && info.Blob == dp.Blobsum) {
			break
		}
		layerTypes[len(diffPairs)-1-i] = info.MediaType

		parent := tref.Parent()
		tref.Release(context.TODO())
		tref = parent
		if tref == nil {
			break
		}
	}
	if tref != nil {
		tref.Release(context.TODO())
	}
	return layerTypes
}

var toDockerLayerType = map[string]string{
	ocispec.MediaTypeImageLayer:            images.MediaTypeDockerSchema2Layer,
	images.MediaTypeDockerSchema2Layer:     images.MediaTypeDockerSchema2Layer,
	ocispec.MediaTypeImageLayerGzip:        images.MediaTypeDockerSchema2LayerGzip,
	images.MediaTypeDockerSchema2LayerGzip: images.MediaTypeDockerSchema2LayerGzip,
}

var toOCILayerType = map[string]string{
	ocispec.MediaTypeImageLayer:            ocispec.MediaTypeImageLayer,
	images.MediaTypeDockerSchema2Layer:     ocispec.MediaTypeImageLayer,
	ocispec.MediaTypeImageLayerGzip:        ocispec.MediaTypeImageLayerGzip,
	images.MediaTypeDockerSchema2LayerGzip: ocispec.MediaTypeImageLayerGzip,
}

func ConvertLayerMediaType(mediaType string, oci bool) string {
	var converted string
	if oci {
		converted = toOCILayerType[mediaType]
	} else {
		converted = toDockerLayerType[mediaType]
	}
	if converted == "" {
		logrus.Warnf("unhandled conversion for mediatype %q", mediaType)
		return mediaType
	}
	return converted
}
