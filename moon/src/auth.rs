//! HMAC 认证
//!
//! 负责：
//! - Moon 与 Controller 之间的 HMAC-SHA256 认证
//! - API Key / API Secret 验证
//! - Nonce + Timestamp 防重放

use anyhow::Result;
use hmac::{Hmac, Mac};
use sha2::Sha256;

type HmacSha256 = Hmac<Sha256>;

/// 认证消息结构
#[derive(Debug, Clone)]
pub struct AuthMessage {
    pub device_id: String,
    pub timestamp: u64,
    pub nonce: String,
    pub body_hash: String,
}

/// HMAC 认证器
pub struct HmacAuth {
    api_key: String,
    api_secret: Vec<u8>,
}

impl HmacAuth {
    pub fn new(api_key: String, api_secret: String) -> Self {
        Self {
            api_key,
            api_secret: api_secret.into_bytes(),
        }
    }

    /// 生成认证签名
    ///
    /// 签名内容：nonce + timestamp + method + path + body (与 Controller 一致)
    pub fn sign(&self, method: &str, path: &str, timestamp: u64, nonce: &str, body: &str) -> String {
        let message = format!("{}{}{}{}{}", nonce, timestamp, method, path, body);

        let mut mac = HmacSha256::new_from_slice(&self.api_secret).unwrap();
        mac.update(message.as_bytes());

        let result = mac.finalize();
        hex::encode(result.into_bytes())
    }

    /// 验证认证签名
    pub fn verify(
        &self,
        method: &str,
        path: &str,
        timestamp: u64,
        nonce: &str,
        body_hash: &str,
        signature: &str,
    ) -> Result<bool, AuthError> {
        // 检查时间戳（允许 5 分钟偏差）
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs();

        if (now as i64 - timestamp as i64).abs() > 300 {
            return Err(AuthError::TimestampExpired);
        }

        let expected = self.sign(method, path, timestamp, nonce, body_hash);

        // 使用常量时间比较防止时序攻击
        Ok(constant_time_compare(&expected, signature))
    }

    /// 生成 API Key 认证头
    pub fn auth_header(&self, method: &str, path: &str, body: &[u8]) -> Result<String, AuthError> {
        let timestamp = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs();

        let nonce = generate_nonce();
        let body_hash = sha256_hex(body);

        let signature = self.sign(method, path, timestamp, &nonce, &body_hash);

        // 格式: Moon api_key timestamp nonce signature
        Ok(format!(
            "Moon {} {} {} {}",
            self.api_key, timestamp, nonce, signature
        ))
    }

    /// 获取 API Key
    pub fn api_key(&self) -> &str {
        &self.api_key
    }
}

/// SHA256 哈希
pub fn sha256_hex(data: &[u8]) -> String {
    use sha2::Digest;
    let hash = Sha256::digest(data);
    hex::encode(hash)
}

/// 生成随机 Nonce
pub fn generate_nonce() -> String {
    use rand::Rng;
    let mut nonce = [0u8; 16];
    rand::thread_rng().fill(&mut nonce);
    hex::encode(nonce)
}

/// 常数时间字符串比较
fn constant_time_compare(a: &str, b: &str) -> bool {
    if a.len() != b.len() {
        return false;
    }

    let mut result = 0u8;
    for (x, y) in a.bytes().zip(b.bytes()) {
        result |= x ^ y;
    }
    result == 0
}

/// 认证错误类型
#[derive(Debug, thiserror::Error)]
pub enum AuthError {
    #[error("API Key 不匹配")]
    ApiKeyMismatch,

    #[error("签名验证失败")]
    SignatureMismatch,

    #[error("时间戳已过期")]
    TimestampExpired,

    #[error("Nonce 已使用")]
    NonceReused,
}

/// 从请求头解析认证信息
pub fn parse_auth_header(header: &str) -> Option<(String, u64, String, String)> {
    // 格式: Moon api_key timestamp nonce signature
    let parts: Vec<&str> = header.split_whitespace().collect();
    if parts.len() != 5 || parts[0] != "Moon" {
        return None;
    }

    let api_key = parts[1].to_string();
    let timestamp: u64 = parts[2].parse().ok()?;
    let nonce = parts[3].to_string();
    let signature = parts[4].to_string();

    Some((api_key, timestamp, nonce, signature))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_sign_and_verify() {
        let auth = HmacAuth::new(
            "test_api_key".to_string(),
            "test_api_secret".to_string(),
        );

        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs();

        let signature = auth.sign("POST", "/api/v1/moon/register", now, "test_nonce", "body_hash");

        assert!(auth
            .verify("POST", "/api/v1/moon/register", now, "test_nonce", "body_hash", &signature)
            .unwrap());
    }

    #[test]
    fn test_constant_time_compare() {
        assert!(constant_time_compare("hello", "hello"));
        assert!(!constant_time_compare("hello", "world"));
        assert!(!constant_time_compare("hello", "hell"));
    }

    #[test]
    fn test_sha256() {
        let hash = sha256_hex(b"hello world");
        assert_eq!(hash, "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9");
    }
}
