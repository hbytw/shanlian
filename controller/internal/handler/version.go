package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/service"
)

type VersionHandler struct {
	versionService *service.VersionService
}

func NewVersionHandler(versionService *service.VersionService) *VersionHandler {
	return &VersionHandler{versionService: versionService}
}

// ListVersions 列出版本
func (h *VersionHandler) ListVersions(c *gin.Context) {
	platform := c.Query("platform")
	enabledOnly := c.Query("enabled") == "true"

	versions, err := h.versionService.List(c.Request.Context(), platform, enabledOnly)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取版本列表失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    versions,
	})
}

// UploadVersion 上传版本
func (h *VersionHandler) UploadVersion(c *gin.Context) {
	operatorID, _ := uuid.Parse(c.GetString("user_id"))

	// 解析 multipart 表单
	if err := c.Request.ParseMultipartForm(100 << 20); err != nil { // 100MB limit
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"解析上传表单失败："+err.Error(),
			nil,
		))
		return
	}

	// 获取表单字段
	version := c.PostForm("version")
	minSupported := c.PostForm("min_supported")
	platform := c.PostForm("platform")
	forceUpdate := c.PostForm("force_update") == "true"

	if version == "" || minSupported == "" || platform == "" {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"缺少必填字段",
			nil,
		))
		return
	}

	// 获取文件
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请选择要上传的文件",
			nil,
		))
		return
	}
	defer file.Close()

	// 计算 SHA256
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"计算文件哈希失败",
			nil,
		))
		return
	}
	sha256Str := hex.EncodeToString(hash.Sum(nil))

	// 重新获取文件内容
	file.Seek(0, 0)

	req := &model.VersionCreateRequest{
		Version:      version,
		MinSupported: minSupported,
		Platform:     platform,
		ForceUpdate:  forceUpdate,
		SHA256:       sha256Str,
		FileSize:     header.Size,
		DownloadURL: "", // 将由 service 生成
	}

	v, err := h.versionService.Create(c.Request.Context(), req, file, operatorID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"创建版本失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusCreated, model.SuccessResponse{
		Message: "版本上传成功",
		Data:    v,
	})
}

// DeleteVersion 删除版本
func (h *VersionHandler) DeleteVersion(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的版本 ID",
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))

	if err := h.versionService.Delete(c.Request.Context(), id, operatorID); err != nil {
		if err == service.ErrVersionNotFound {
			c.JSON(http.StatusNotFound, model.NewErrorResponse(
				model.ErrSystemInternal,
				"版本不存在",
				nil,
			))
		} else {
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
				model.ErrSystemInternal,
				"删除版本失败："+err.Error(),
				nil,
			))
		}
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "删除成功",
	})
}

// ServeVersionFile 提供版本文件下载
func (h *VersionHandler) ServeVersionFile(c *gin.Context) {
	filename := c.Param("filename")
	versionDir := os.Getenv("VERSION_UPLOAD_DIR")
	if versionDir == "" {
		versionDir = "/tmp/versions"
	}

	filePath := filepath.Join(versionDir, filename)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, model.NewErrorResponse(
			model.ErrSystemInternal,
			"文件不存在",
			nil,
		))
		return
	}

	c.File(filePath)
}
