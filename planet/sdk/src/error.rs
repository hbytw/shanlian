//! SDK 错误处理模块

use std::fmt;

/// SDK 错误类型
#[derive(Debug, Clone)]
pub enum SdkError {
    InvalidParam(String),
    NotInitialized,
    AlreadyInitialized,
    ConnectFailed(String),
    AuthFailed(String),
    Timeout,
    Network(String),
    Internal(String),
}

impl fmt::Display for SdkError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            SdkError::InvalidParam(msg) => write!(f, "Invalid parameter: {}", msg),
            SdkError::NotInitialized => write!(f, "SDK not initialized"),
            SdkError::AlreadyInitialized => write!(f, "SDK already initialized"),
            SdkError::ConnectFailed(msg) => write!(f, "Connection failed: {}", msg),
            SdkError::AuthFailed(msg) => write!(f, "Authentication failed: {}", msg),
            SdkError::Timeout => write!(f, "Operation timeout"),
            SdkError::Network(msg) => write!(f, "Network error: {}", msg),
            SdkError::Internal(msg) => write!(f, "Internal error: {}", msg),
        }
    }
}

impl std::error::Error for SdkError {}

pub type SdkResult<T> = Result<T, SdkError>;
