package channel

import (
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/worryzyy/upstream-hub/internal/crypto"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

// newTestService 用内存 SQLite 建一套最小依赖的 Service。
func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := storage.AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cipher, err := crypto.NewCipher("test-key-test-key-test-key-32b!")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	return NewService(
		storage.NewChannels(db),
		storage.NewAuthSessions(db),
		storage.NewCaptchas(db),
		storage.NewMonitorLogs(db),
		cipher,
	)
}

func mustCreate(t *testing.T, s *Service, name string) *storage.Channel {
	t.Helper()
	c, err := s.Create(CreateInput{
		Name:     name,
		Type:     storage.ChannelTypeNewAPI,
		SiteURL:  "https://example.com",
		Username: "u",
		Password: "p",
	})
	if err != nil {
		t.Fatalf("create channel %q: %v", name, err)
	}
	return c
}

// 回归：删除渠道后允许新建同名渠道（软删行不得占用 name 唯一索引）。
func TestDeleteThenRecreateSameName(t *testing.T) {
	s := newTestService(t)

	c := mustCreate(t, s, "dup")
	if err := s.Delete(c.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// 同名渠道应能直接新建
	recreated := mustCreate(t, s, "dup")
	if recreated.ID == 0 {
		t.Fatal("recreated channel has no ID")
	}
}

// 存量脏数据场景：库里残留旧版本软删除的同名行，新建同名渠道仍应成功。
func TestCreateWhenLegacySoftDeletedRowExists(t *testing.T) {
	s := newTestService(t)

	c := mustCreate(t, s, "legacy")
	// 模拟旧版本行为：绕过 Service，直接打软删标记
	if err := s.Channels.SoftDeleteForTest(c.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	recreated := mustCreate(t, s, "legacy")
	if recreated.ID == 0 {
		t.Fatal("recreated channel has no ID")
	}
}

// 重复名称应返回 ErrDuplicateName 而非数据库层错误。
func TestCreateDuplicateActiveName(t *testing.T) {
	s := newTestService(t)
	mustCreate(t, s, "dup")

	_, err := s.Create(CreateInput{
		Name:     "dup",
		Type:     storage.ChannelTypeNewAPI,
		SiteURL:  "https://example.com",
		Username: "u",
		Password: "p",
	})
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("want ErrDuplicateName, got %v", err)
	}
}
