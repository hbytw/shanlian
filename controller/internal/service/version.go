package service

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/repository"
)

var (
	ErrVersionNotFound = errors.New("版本不存在")
	ErrVersionExists   = errors.New("版本号已存在")
)

// VersionService 版本服务
type VersionService struct {
	versionRepo *repository.VersionRepository
	auditRepo   *repository.AuditRepository
	uploadDir   string
}

func NewVersionService(versionRepo *repository.VersionRepository, auditRepo *repository.AuditRepository, uploadDir string) *VersionService {
	if uploadDir == "" {
		uploadDir = "/tmp/versions"
	}
	os.MkdirAll(uploadDir, 0755)
	return &VersionService{
		versionRepo: versionRepo,
		auditRepo:   auditRepo,
		uploadDir:   uploadDir,
	}
}

// List 获取版本列表
func (s *VersionService) List(ctx context.Context, platform string, enabledOnly bool) ([]*model.Version, error) {
	return s.versionRepo.List(ctx, platform, enabledOnly)
}

// GetLatest 获取最新版本
func (s *VersionService) GetLatest(ctx context.Context, platform string) (*model.Version, error) {
	return s.versionRepo.GetLatest(ctx, platform)
}

// GetByID 根据 ID 获取
func (s *VersionService) GetByID(ctx context.Context, id uuid.UUID) (*model.Version, error) {
	return s.versionRepo.GetByID(ctx, id)
}

// Create 创建版本
func (s *VersionService) Create(ctx context.Context, req *model.VersionCreateRequest, file io.Reader, operatorID uuid.UUID) (*model.Version, error) {
	// 生成文件路径
	filename := filepath.Join(s.uploadDir, platformFileName(req.Platform, req.Version))
	f, err := os.Create(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// 写入文件
	if _, err := io.Copy(f, file); err != nil {
		return nil, err
	}

	// 生成版本记录
	version := &model.Version{
		ID:           uuid.New(),
		Version:      req.Version,
		MinSupported: req.MinSupported,
		DownloadURL:   "/versions/" + platformFileName(req.Platform, req.Version),
		SHA256:       req.SHA256,
		FileSize:     req.FileSize,
		Platform:     req.Platform,
		ForceUpdate:  req.ForceUpdate,
		Enabled:      true,
		CreatedBy:    &operatorID,
	}

	if err := s.versionRepo.Create(ctx, version); err != nil {
		os.Remove(filename)
		return nil, err
	}

	// 记录日志
	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           uuid.New(),
		OperatorID:   operatorID,
		Action:       "创建版本",
		ResourceType: "version",
		ResourceID:   &version.ID,
		ResourceName: &version.Version,
	})

	return version, nil
}

// Delete 删除版本
func (s *VersionService) Delete(ctx context.Context, id uuid.UUID, operatorID uuid.UUID) error {
	version, err := s.versionRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if version == nil {
		return ErrVersionNotFound
	}

	if err := s.versionRepo.Delete(ctx, id); err != nil {
		return err
	}

	// 删除文件
	filename := filepath.Join(s.uploadDir, platformFileName(version.Platform, version.Version))
	os.Remove(filename)

	// 记录日志
	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           uuid.New(),
		OperatorID:   operatorID,
		Action:       "删除版本",
		ResourceType: "version",
		ResourceID:   &id,
		ResourceName: &version.Version,
	})

	return nil
}

func platformFileName(platform, version string) string {
	ext := map[string]string{
		"linux":   ".deb",
		"windows": ".exe",
		"darwin":  ".dmg",
		"android": ".apk",
		"ios":     ".ipa",
	}
	if e, ok := ext[platform]; ok {
		return platform + "_" + version + e
	}
	return platform + "_" + version
}
