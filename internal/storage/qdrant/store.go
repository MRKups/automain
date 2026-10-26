// Package qdrant implements the derived vector port using Qdrant's official gRPC client.
package qdrant

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MRKups/automain/internal/domain"
	q "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxBatch       = 256
	maxChunkBytes  = 64 * 1024
	maxBatchBytes  = 8 * 1024 * 1024
	requestTimeout = 30 * time.Second
)

type Options struct {
	Host             string
	Port             int
	TLS              bool
	APIKey           string
	CollectionPrefix string
}

type Store struct {
	client *q.GrpcClient
	prefix string
}

var _ domain.VectorStore = (*Store)(nil)

// New allocates a lazy connection. Only explicit operations contact Qdrant.
func New(options Options) (*Store, error) {
	const op = "qdrant.configure"
	if options.Host == "" {
		options.Host = "localhost"
	}
	if options.Port == 0 {
		options.Port = 6334
	}
	if strings.TrimSpace(options.Host) != options.Host || strings.ContainsAny(options.Host, "/\\?#@ \t\r\n") || (strings.Contains(options.Host, ":") && net.ParseIP(options.Host) == nil) || options.Port < 1 || options.Port > 65535 {
		return nil, invalid(op, "Qdrant requires a hostname or bare IP address and a port between 1 and 65535.", nil)
	}
	if len(options.CollectionPrefix) > 128 {
		return nil, invalid(op, "Qdrant collection prefix exceeds 128 bytes.", nil)
	}
	for _, r := range options.CollectionPrefix {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return nil, invalid(op, "Qdrant collection prefix accepts only letters, digits, underscores, and hyphens.", nil)
		}
	}
	client, err := q.NewGrpcClient(&q.Config{
		Host: options.Host, Port: options.Port, UseTLS: options.TLS, APIKey: options.APIKey,
		SkipCompatibilityCheck: true,
		GrpcOptions:            []grpc.DialOption{grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16*1024*1024), grpc.MaxCallSendMsgSize(16*1024*1024))},
	})
	if err != nil {
		return nil, unavailable(op, err)
	}
	return &Store{client: client, prefix: options.CollectionPrefix}, nil
}

func (s *Store) Close() error {
	if err := s.client.Close(); err != nil {
		return unavailable("qdrant.close", err)
	}
	return nil
}

// Check uses Qdrant's own health RPC, not a model call or a local connection state.
func (s *Store) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	reply, err := s.client.Qdrant().HealthCheck(ctx, &q.HealthCheckRequest{})
	if err != nil {
		return unavailable("qdrant.health", err)
	}
	if reply == nil || reply.GetVersion() == "" {
		return malformed("qdrant.health")
	}
	return nil
}

func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, requestTimeout)
}

func validateScope(op, repo string, space domain.EmbeddingSpace) error {
	if err := domain.ValidateRepoSlug(repo); err != nil {
		return invalid(op, "Qdrant operation requires a valid repository slug.", err)
	}
	if err := space.Validate(); err != nil {
		return invalid(op, "Qdrant operation requires a valid embedding space.", err)
	}
	if len(repo) > 256 || len(space.Provider)+len(space.Model)+len(space.Revision) > 4096 {
		return invalid(op, "Qdrant repository or embedding-space metadata exceeds its size limit.", nil)
	}
	if !utf8.ValidString(repo) || !utf8.ValidString(space.Provider) || !utf8.ValidString(space.Model) || !utf8.ValidString(space.Revision) {
		return invalid(op, "Qdrant repository and embedding-space metadata must be valid UTF-8.", nil)
	}
	return nil
}

func invalid(op, message string, err error) error {
	return domain.NewError(domain.ErrorCodeInvalidInput, op, message, "Correct the operation inputs and retry.", err)
}

func unavailable(op string, err error) error {
	return domain.NewError(domain.ErrorCodeExternalUnavailable, op,
		fmt.Sprintf("Qdrant request failed (%s).", status.Code(err)),
		"Check Qdrant availability, credentials, and server logs, then retry; writes may need reconciliation.", fmt.Errorf("Qdrant RPC: %w", err))
}

func malformed(op string) error {
	return domain.NewError(domain.ErrorCodeExternalUnavailable, op, "Qdrant returned an invalid or incompatible response.", "Inspect the collection configuration and payloads; restore or rebuild the derived index before retrying.", nil)
}

func missing(op string) error {
	return domain.NewError(domain.ErrorCodeNotFound, op, "The stored query passage or its dense vector is missing or stale.", "Run index reconcile, then restore or migrate saved vectors, or run index embed, before retrying similarity.", nil)
}

func missingCollection(op string, err error) error {
	return domain.NewError(domain.ErrorCodeExternalUnavailable, op, "Indexed Qdrant collection is missing.", "Run index reconcile for the repository, then restore or migrate saved vectors, or run index embed, before retrying search.", err)
}

// Collection metadata is reread, rather than cached, so drops and schema changes
// cannot leave a process trusting an obsolete collection or payload-index cache.
func (s *Store) collection(ctx context.Context, op string, space domain.EmbeddingSpace, create bool) (*q.CollectionInfo, error) {
	name := s.prefix + space.ID()
	reply, err := s.client.Collections().Get(ctx, &q.GetCollectionInfoRequest{CollectionName: name})
	if status.Code(err) == codes.NotFound {
		if !create {
			return nil, nil
		}
		created, createErr := s.client.Collections().Create(ctx, &q.CreateCollection{
			CollectionName: name,
			VectorsConfig:  q.NewVectorsConfig(&q.VectorParams{Size: uint64(space.Dimensions), Distance: q.Distance_Cosine, Datatype: q.PtrOf(q.Datatype_Float32)}),
			ShardNumber:    q.PtrOf(uint32(1)), ReplicationFactor: q.PtrOf(uint32(1)),
		})
		if createErr != nil && status.Code(createErr) != codes.AlreadyExists {
			return nil, unavailable(op, createErr)
		}
		if createErr == nil && (created == nil || !created.GetResult()) {
			return nil, malformed(op)
		}
		reply, err = s.client.Collections().Get(ctx, &q.GetCollectionInfoRequest{CollectionName: name})
	}
	if err != nil {
		return nil, unavailable(op, err)
	}
	info := reply.GetResult()
	params := info.GetConfig().GetParams()
	vector := params.GetVectorsConfig().GetParams()
	if vector == nil || vector.GetSize() != uint64(space.Dimensions) || vector.GetDistance() != q.Distance_Cosine || (vector.GetDatatype() != q.Datatype_Default && vector.GetDatatype() != q.Datatype_Float32) || vector.GetMultivectorConfig() != nil || vector.GetQuantizationConfig() != nil || info.GetConfig().GetQuantizationConfig() != nil || len(params.GetSparseVectorsConfig().GetMap()) != 0 {
		return nil, malformed(op)
	}
	if create {
		for _, field := range []string{"repository", "item_key", "item_kind"} {
			if index, exists := info.GetPayloadSchema()[field]; exists {
				if index == nil || index.GetDataType() != q.PayloadSchemaType_Keyword {
					return nil, malformed(op)
				}
				continue
			}
			indexed, indexErr := s.client.Points().CreateFieldIndex(ctx, &q.CreateFieldIndexCollection{CollectionName: name, Wait: q.PtrOf(true), FieldName: field, FieldType: q.PtrOf(q.FieldType_FieldTypeKeyword)})
			if indexErr != nil {
				return nil, unavailable(op, indexErr)
			}
			if !completed(indexed.GetResult()) {
				return nil, malformed(op)
			}
		}
	}
	return info, nil
}

func completed(result *q.UpdateResult) bool {
	return result != nil && result.GetStatus() == q.UpdateStatus_Completed
}
