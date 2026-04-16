package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/crypto"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/repository"
	"github.com/hbytw/shanlian/controller/pkg/jwt"
	"github.com/hbytw/shanlian/controller/pkg/verifycode"
)

var (
	ErrInvalidCredentials = errors.New("用户名或密码错误")
	ErrUserDisabled       = errors.New("账号已被禁用")
	ErrUserNotFound       = errors.New("用户不存在")
	ErrEmailExists        = errors.New("邮箱已被注册")
	ErrUsernameExists     = errors.New("用户名已被注册")
	ErrPhoneExists        = errors.New("手机号已被注册")
	ErrCodeRateLimited    = errors.New("发送频率超限，请稍后再试")
	ErrCodeExpired        = errors.New("验证码已过期")
	ErrCodeInvalid        = errors.New("验证码错误")
	ErrCodeSendFailed     = errors.New("验证码发送失败")
	ErrInvalidTarget      = errors.New("无效的发送目标，请提供手机号或邮箱")
	ErrOAuthNotSupported  = errors.New("暂不支持该 OAuth 登录方式")
)

// AuthService 认证服务
type AuthService struct {
	userRepo           *repository.UserRepository
	jwtManager         *jwt.JWTManager
	auditRepo          *repository.AuditRepository
	verifyCodeManager  *verifycode.VerifyCodeManager
}

func NewAuthService(userRepo *repository.UserRepository, jwtManager *jwt.JWTManager, auditRepo *repository.AuditRepository, verifyCodeManager *verifycode.VerifyCodeManager) *AuthService {
	return &AuthService{
		userRepo:          userRepo,
		jwtManager:        jwtManager,
		auditRepo:         auditRepo,
		verifyCodeManager: verifyCodeManager,
	}
}

func (s *AuthService) Register(ctx context.Context, req *model.RegisterRequest) (*model.User, error) {
	// 检查用户名是否存在
	existingUser, err := s.userRepo.GetByUsername(ctx, req.Username)
	if err != nil {
		return nil, err
	}
	if existingUser != nil {
		return nil, ErrUsernameExists
	}

	// 检查邮箱是否存在
	existingUser, err = s.userRepo.GetByEmail(ctx, req.Email)
	if err != nil {
		return nil, err
	}
	if existingUser != nil {
		return nil, ErrEmailExists
	}

	// 哈希密码
	hashedPassword, err := crypto.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	// 创建用户
	userCreateReq := &model.UserCreateRequest{
		Username: req.Username,
		Email:    req.Email,
		Password: hashedPassword,
		Role:     "user",
	}

	user, err := s.userRepo.Create(ctx, userCreateReq, hashedPassword)
	if err != nil {
		return nil, err
	}

	return user, nil
}

func (s *AuthService) Login(ctx context.Context, req *model.LoginRequest) (*model.LoginResponse, error) {
	user, err := s.userRepo.GetByUsername(ctx, req.Username)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrInvalidCredentials
	}

	if !user.Enabled {
		return nil, ErrUserDisabled
	}

	if !crypto.CheckPassword(req.Password, user.PasswordHash) {
		return nil, ErrInvalidCredentials
	}

	// 生成令牌
	accessToken, err := s.jwtManager.GenerateAccessToken(user.ID.String(), user.Username, user.Role)
	if err != nil {
		return nil, err
	}

	refreshToken, err := s.jwtManager.GenerateRefreshToken(user.ID.String())
	if err != nil {
		return nil, err
	}

	// 更新最后登录时间
	s.userRepo.UpdateLastLogin(ctx, user.ID)

	// 记录操作日志
	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           uuid.New(),
		OperatorID:   user.ID,
		OperatorName: &user.Username,
		Action:       "登录",
		ResourceType: "auth",
	})

	return &model.LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(s.jwtManager.GetAccessTokenTTL().Seconds()),
		TokenType:    "Bearer",
		User:         user,
	}, nil
}

func (s *AuthService) Refresh(ctx context.Context, req *model.RefreshRequest) (*model.LoginResponse, error) {
	userID, err := s.jwtManager.ValidateRefreshToken(req.RefreshToken)
	if err != nil {
		return nil, err
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, ErrUserNotFound
	}

	user, err := s.userRepo.GetByID(ctx, userUUID)
	if err != nil || user == nil {
		return nil, ErrUserNotFound
	}

	if !user.Enabled {
		return nil, ErrUserDisabled
	}

	accessToken, err := s.jwtManager.GenerateAccessToken(user.ID.String(), user.Username, user.Role)
	if err != nil {
		return nil, err
	}

	refreshToken, err := s.jwtManager.GenerateRefreshToken(user.ID.String())
	if err != nil {
		return nil, err
	}

	return &model.LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(s.jwtManager.GetAccessTokenTTL().Seconds()),
		TokenType:    "Bearer",
	}, nil
}

func (s *AuthService) GetClientVersion(ctx context.Context, platform string) (*model.Version, error) {
	// TODO: 实现版本查询
	return nil, nil
}

// LoginByPhone 手机号登录
func (s *AuthService) LoginByPhone(ctx context.Context, phone, code string) (*model.LoginResponse, error) {
	// 验证验证码
	if s.verifyCodeManager == nil {
		return nil, errors.New("验证码服务未初始化")
	}

	valid, err := s.verifyCodeManager.Verify(ctx, "phone:"+phone, code)
	if err != nil {
		return nil, errors.New("验证验证码失败")
	}
	if !valid {
		return nil, ErrCodeInvalid
	}

	// 根据手机号获取用户
	user, err := s.userRepo.GetByPhone(ctx, phone)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrUserNotFound
	}

	if !user.Enabled {
		return nil, ErrUserDisabled
	}

	return s.generateLoginResponse(ctx, user)
}

// LoginByEmail 邮箱登录
func (s *AuthService) LoginByEmail(ctx context.Context, email, code string) (*model.LoginResponse, error) {
	// 验证验证码
	if s.verifyCodeManager == nil {
		return nil, errors.New("验证码服务未初始化")
	}

	valid, err := s.verifyCodeManager.Verify(ctx, "email:"+email, code)
	if err != nil {
		return nil, errors.New("验证验证码失败")
	}
	if !valid {
		return nil, ErrCodeInvalid
	}

	// 根据邮箱获取用户
	user, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrUserNotFound
	}

	if !user.Enabled {
		return nil, ErrUserDisabled
	}

	return s.generateLoginResponse(ctx, user)
}

// ResetPasswordByCode 通过验证码重置密码
func (s *AuthService) ResetPasswordByCode(ctx context.Context, target, code, newPassword string) error {
	if s.verifyCodeManager == nil {
		return errors.New("验证码服务未初始化")
	}

	// 验证验证码
	valid, err := s.verifyCodeManager.Verify(ctx, "reset:"+target, code)
	if err != nil {
		return errors.New("验证验证码失败")
	}
	if !valid {
		return ErrCodeInvalid
	}

	// 获取用户
	var user *model.User
	if target != "" {
		if contains(target, "@") {
			user, err = s.userRepo.GetByEmail(ctx, target)
		} else {
			user, err = s.userRepo.GetByPhone(ctx, target)
		}
	}
	if err != nil {
		return err
	}
	if user == nil {
		return ErrUserNotFound
	}

	// 哈希新密码
	hashedPassword, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}

	// 更新密码
	if err := s.userRepo.UpdatePassword(ctx, user.ID, hashedPassword); err != nil {
		return err
	}

	// 记录操作日志
	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           s.auditRepo.CreateAdminLogID(),
		OperatorID:   user.ID,
		OperatorName: &user.Username,
		Action:       "通过验证码重置密码",
		ResourceType: "user",
		ResourceID:   &user.ID,
	})

	return nil
}

// SendCode 发送验证码
func (s *AuthService) SendCode(ctx context.Context, target, codeType string) error {
	if s.verifyCodeManager == nil {
		return errors.New("验证码服务未初始化")
	}

	// 检查频率限制
	allowed, remaining, err := s.verifyCodeManager.SendRateLimit(ctx, target)
	if err != nil {
		return errors.New("检查发送频率失败")
	}
	if !allowed {
		return errors.New("发送频率超限，请 " + string(rune(remaining)) + " 秒后再试")
	}

	// 检查每日次数限制
	allowed, err = s.verifyCodeManager.CheckDailyLimit(ctx, target)
	if err != nil {
		return errors.New("检查每日次数失败")
	}
	if !allowed {
		return errors.New("今日发送次数已用尽，请明天再试")
	}

	// 生成验证码
	generatedCode := s.verifyCodeManager.Generate()

	// 保存验证码
	key := codeType + ":" + target
	if err := s.verifyCodeManager.Store(ctx, key, generatedCode); err != nil {
		return errors.New("保存验证码失败")
	}

	// 记录发送
	if err := s.verifyCodeManager.RecordSend(ctx, target); err != nil {
		return errors.New("记录发送失败")
	}

	// TODO: 实际发送验证码（邮件/短信）
	// 这里仅记录日志，生产环境需要接入实际的邮件/短信服务
	fmt.Printf("发送验证码到 %s，验证码：%s（生产环境请替换为实际发送）", target, generatedCode)

	return nil
}

// OAuthLogin OAuth 第三方登录（预留）
func (s *AuthService) OAuthLogin(ctx context.Context, provider, code string) (*model.LoginResponse, error) {
	// 预留结构，实际实现需要根据不同的 OAuth 提供商
	return nil, ErrOAuthNotSupported
}

// generateLoginResponse 生成登录响应
func (s *AuthService) generateLoginResponse(ctx context.Context, user *model.User) (*model.LoginResponse, error) {
	accessToken, err := s.jwtManager.GenerateAccessToken(user.ID.String(), user.Username, user.Role)
	if err != nil {
		return nil, err
	}

	refreshToken, err := s.jwtManager.GenerateRefreshToken(user.ID.String())
	if err != nil {
		return nil, err
	}

	// 更新最后登录时间
	s.userRepo.UpdateLastLogin(ctx, user.ID)

	// 记录操作日志
	s.auditRepo.LogAdmin(ctx, &model.AdminLog{
		ID:           uuid.New(),
		OperatorID:   user.ID,
		OperatorName: &user.Username,
		Action:       "登录",
		ResourceType: "auth",
	})

	return &model.LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(s.jwtManager.GetAccessTokenTTL().Seconds()),
		TokenType:    "Bearer",
		User:         user,
	}, nil
}

// contains 判断字符串是否包含子串
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsImpl(s, substr))
}

func containsImpl(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
