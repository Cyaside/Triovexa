package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

var ErrInvalidCredentials = errors.New("invalid username or password")

type Service struct {
	repository storage.Repository
	sessionTTL time.Duration
	now        func() time.Time
}

type SessionGrant struct {
	Token     string
	CSRFToken string
	ExpiresAt time.Time
	User      domain.User
}

func NewService(repository storage.Repository, sessionTTL time.Duration) *Service {
	if sessionTTL <= 0 {
		sessionTTL = 12 * time.Hour
	}
	return &Service{repository: repository, sessionTTL: sessionTTL, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) CreateUser(ctx context.Context, username string, password string, role string) (domain.User, error) {
	username = strings.TrimSpace(username)
	if len(username) < 3 || len(password) < 12 {
		return domain.User{}, errors.New("username must have at least 3 characters and password at least 12 characters")
	}
	if !validRole(role) {
		return domain.User{}, errors.New("role must be viewer, operator, or admin")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return domain.User{}, err
	}
	user := domain.User{ID: uuid.NewString(), Username: username, PasswordHash: hash, Role: role, CreatedAt: s.now()}
	if err := s.repository.CreateUser(ctx, user); err != nil {
		return domain.User{}, err
	}
	user.PasswordHash = ""
	return user, nil
}

func (s *Service) Login(ctx context.Context, username string, password string) (SessionGrant, error) {
	user, err := s.repository.GetUserByUsername(ctx, strings.TrimSpace(username))
	if err != nil || !verifyPassword(password, user.PasswordHash) {
		return SessionGrant{}, ErrInvalidCredentials
	}
	token, err := randomToken(32)
	if err != nil {
		return SessionGrant{}, err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return SessionGrant{}, err
	}
	now := s.now()
	session := domain.Session{TokenHash: tokenHash(token), UserID: user.ID, CSRFHash: tokenHash(csrf), ExpiresAt: now.Add(s.sessionTTL), CreatedAt: now}
	if err := s.repository.CreateSession(ctx, session); err != nil {
		return SessionGrant{}, err
	}
	user.PasswordHash = ""
	return SessionGrant{Token: token, CSRFToken: csrf, ExpiresAt: session.ExpiresAt, User: user}, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (domain.User, domain.Session, error) {
	if strings.TrimSpace(token) == "" {
		return domain.User{}, domain.Session{}, ErrInvalidCredentials
	}
	session, err := s.repository.GetSession(ctx, tokenHash(token))
	if err != nil || !session.ExpiresAt.After(s.now()) {
		return domain.User{}, domain.Session{}, ErrInvalidCredentials
	}
	user, err := s.repository.GetUser(ctx, session.UserID)
	if err != nil {
		return domain.User{}, domain.Session{}, ErrInvalidCredentials
	}
	user.PasswordHash = ""
	return user, session, nil
}

func (s *Service) ValidateCSRF(session domain.Session, token string) bool {
	expected := []byte(session.CSRFHash)
	actual := []byte(tokenHash(token))
	return len(expected) == len(actual) && subtle.ConstantTimeCompare(expected, actual) == 1
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.repository.DeleteSession(ctx, tokenHash(token))
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	memory, iterations, parallelism := uint32(64*1024), uint32(3), uint8(2)
	hash := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func verifyPassword(password string, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var memory, iterations uint64
	var parallelism uint64
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) == 0 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, uint32(iterations), uint32(memory), uint8(parallelism), uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func randomToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func tokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func validRole(role string) bool {
	switch role {
	case domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin:
		return true
	default:
		return false
	}
}

func ParseRole(value string) (string, error) {
	role := strings.ToLower(strings.TrimSpace(value))
	if !validRole(role) {
		return "", fmt.Errorf("invalid role %s", strconv.Quote(value))
	}
	return role, nil
}
