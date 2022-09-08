package grpcutil

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/o1egl/paseto"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/config"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/user"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
)

const (
	// nolint:gosec // These are not potential hardcoded credentials.
	gatewayTokenHeader    = "grpcgateway-authorization"
	allocationTokenHeader = "x-allocation-token"
	userTokenHeader       = "x-user-token"
	cookieName            = "auth"
)

var unauthenticatedMethods = map[string]bool{
	"/determined.api.v1.Determined/Login":        true,
	"/determined.api.v1.Determined/GetMaster":    true,
	"/determined.api.v1.Determined/GetTelemetry": true,
}

var (
	// ErrInvalidCredentials notifies that the provided credentials are invalid or missing.
	ErrInvalidCredentials = status.Error(codes.Unauthenticated, "invalid credentials")
	// ErrTokenMissing notifies that the bearer token could not be found.
	ErrTokenMissing = status.Error(codes.Unauthenticated, "token missing")
	// ErrPermissionDenied notifies that the user does not have permission to access the method.
	ErrPermissionDenied = status.Error(codes.PermissionDenied, "user does not have permission")
)

func allocationSessionByTokenBun(token string) (*model.AllocationSession, error) {
	v2 := paseto.NewV2()

	var session model.AllocationSession
	err := v2.Verify(token, db.GetTokenKeys().PublicKey, &session, nil)
	if err != nil {
		log.WithError(err).Debug("failed to verify allocation_session token")
		return nil, db.ErrNotFound
	}

	err = db.Bun().NewSelect().Model(&session).Where("id = ?", session.ID).Scan(context.Background())
	if errors.Cause(err) == sql.ErrNoRows {
		log.WithField("allocation_sessions.id", session.ID).Debug("allocation_session not found")
		return nil, db.ErrNotFound
	} else if err != nil {
		log.WithError(err).WithField("allocation_sessions.id", session.ID).
			Debug("failed to lookup allocation_session")
		return nil, err
	}

	return &session, nil
}

func getAllocationSessionBun(ctx context.Context) (*model.AllocationSession, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, ErrTokenMissing
	}
	tokens := md[allocationTokenHeader]
	if len(tokens) == 0 {
		return nil, ErrTokenMissing
	}

	token := tokens[0]
	if !strings.HasPrefix(token, "Bearer ") {
		return nil, ErrInvalidCredentials
	}
	token = strings.TrimPrefix(token, "Bearer ")

	switch session, err := allocationSessionByTokenBun(token); err {
	case nil:
		return session, nil
	case db.ErrNotFound:
		return nil, ErrInvalidCredentials
	default:
		return nil, err
	}
}

// GetUser returns the currently logged in user.
func GetUser(ctx context.Context) (*model.User, *model.UserSession, error) {
	extConfig := config.GetMasterConfig().InternalConfig.ExternalSessions

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, nil, ErrTokenMissing
	}
	tokens := md[userTokenHeader]
	if len(tokens) == 0 {
		tokens = md[gatewayTokenHeader]
	}
	if len(tokens) == 0 {
		allocationSession, err := getAllocationSessionBun(ctx)
		if err != nil {
			return nil, nil, err
		}
		if allocationSession.OwnerID == nil {
			return nil, nil, status.Error(codes.InvalidArgument,
				"allocation session has no associated user")
		}
		u, err := user.UserByID(*allocationSession.OwnerID)
		if err != nil {
			return nil, nil, err
		}
		return ptrs.Ptr(u.ToUser()), nil, nil
	}

	token := tokens[0]
	if !strings.HasPrefix(token, "Bearer ") {
		return nil, nil, ErrInvalidCredentials
	}
	token = strings.TrimPrefix(token, "Bearer ")

	var userModel *model.User
	var session *model.UserSession
	var err error
	userModel, session, err = user.UserByToken(token, &extConfig)
	switch err {
	case nil:
		if !userModel.Active {
			return nil, nil, ErrPermissionDenied
		}
		return userModel, session, nil
	case db.ErrNotFound:
		return nil, nil, ErrInvalidCredentials
	default:
		return nil, nil, err
	}
}

// Return error if user cannot be authenticated or lacks authorization.
func auth(ctx context.Context, db *db.PgDB, fullMethod string,
	extConfig *model.ExternalSessions,
) error {
	if unauthenticatedMethods[fullMethod] {
		return nil
	}

	switch _, err := getAllocationSessionBun(ctx); err {
	case ErrTokenMissing:
		// Try user token.
	case nil:
		return nil
	default:
		return err
	}

	if _, _, err := GetUser(ctx); err != nil {
		return err
	}
	return nil
}

func streamAuthInterceptor(db *db.PgDB,
	extConfig *model.ExternalSessions,
) grpc.StreamServerInterceptor {
	return func(
		srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler,
	) error {
		err := auth(ss.Context(), db, info.FullMethod, extConfig)
		if err != nil {
			return err
		}

		return handler(srv, ss)
	}
}

func unaryAuthInterceptor(db *db.PgDB,
	extConfig *model.ExternalSessions,
) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (resp interface{}, err error) {
		err = auth(ctx, db, info.FullMethod, extConfig)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func userTokenResponse(_ context.Context, w http.ResponseWriter, resp proto.Message) error {
	switch r := resp.(type) {
	case *apiv1.LoginResponse:
		http.SetCookie(w, &http.Cookie{
			Name:    cookieName,
			Value:   r.Token,
			Expires: time.Now().Add(db.SessionDuration),
			Path:    "/",
		})
	case *apiv1.LogoutResponse:
		http.SetCookie(w, &http.Cookie{
			Name:    cookieName,
			Value:   "",
			Expires: time.Unix(0, 0),
		})
	}
	return nil
}
