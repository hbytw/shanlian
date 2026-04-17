//! Session 管理
//!
//! 负责：
//! - Session 创建和管理
//! - 防重放（Sequence # 窗口）
//! - 心跳超时检测

use std::collections::HashMap;
use std::sync::Arc;
use std::time::{Duration, Instant};

use tokio::sync::RwLock;

use crate::tunnel::crypto::{generate_nonce, generate_session_id, ChaCha20Cipher, CryptoError};

/// Session state
#[derive(Debug, Clone)]
pub enum SessionState {
    Init,
    HandshakeSent,
    Established,
    Closing,
    Closed,
}

/// Session information
#[derive(Clone)]
pub struct Session {
    pub id: [u8; 8],
    pub state: SessionState,
    pub peer_domain: String,
    pub cipher: ChaCha20Cipher,
    pub local_sequence: u32,
    pub remote_sequence: u32,
    pub created_at: Instant,
    pub last_activity: Instant,
    pub nonce: [u8; 12],
}

impl Session {
    pub fn new(peer_domain: String, key: Vec<u8>) -> Result<Self, CryptoError> {
        let cipher = ChaCha20Cipher::new(&key)?;
        Ok(Self {
            id: generate_session_id(),
            state: SessionState::Init,
            peer_domain,
            cipher,
            local_sequence: 0,
            remote_sequence: 0,
            created_at: Instant::now(),
            last_activity: Instant::now(),
            nonce: generate_nonce(),
        })
    }

    pub fn is_established(&self) -> bool {
        matches!(self.state, SessionState::Established)
    }

    pub fn update_activity(&mut self) {
        self.last_activity = Instant::now();
    }

    pub fn increment_local_sequence(&mut self) -> u32 {
        let seq = self.local_sequence;
        self.local_sequence += 1;
        seq
    }
}

/// Session manager
pub struct SessionManager {
    sessions: Arc<RwLock<HashMap<[u8; 8], Session>>>,
    handshake_timeout: Duration,
    heartbeat_timeout: Duration,
    max_sequence_window: u32,
}

impl SessionManager {
    pub fn new() -> Self {
        Self {
            sessions: Arc::new(RwLock::new(HashMap::new())),
            handshake_timeout: Duration::from_secs(30),
            heartbeat_timeout: Duration::from_secs(90),
            max_sequence_window: 1000,
        }
    }

    /// Create a new session
    pub async fn create_session(
        &self,
        peer_domain: String,
        key: Vec<u8>,
    ) -> Result<[u8; 8], SessionError> {
        let session = Session::new(peer_domain, key)
            .map_err(|e| SessionError::CryptoError(e.to_string()))?;

        let id = session.id;
        self.sessions.write().await.insert(id, session);
        Ok(id)
    }

    /// Get a session by ID
    pub async fn get_session(&self, session_id: &[u8; 8]) -> Option<Session> {
        self.sessions.read().await.get(session_id).cloned()
    }

    /// Update session state
    pub async fn update_state(&self, session_id: &[u8; 8], state: SessionState) -> Result<(), SessionError> {
        let mut sessions = self.sessions.write().await;
        if let Some(session) = sessions.get_mut(session_id) {
            session.state = state;
            session.update_activity();
            Ok(())
        } else {
            Err(SessionError::SessionNotFound)
        }
    }

    /// Check if sequence is valid (anti-replay)
    pub async fn check_sequence(&self, session_id: &[u8; 8], sequence: u32) -> Result<(), SessionError> {
        let sessions = self.sessions.read().await;
        if let Some(session) = sessions.get(session_id) {
            // Accept if sequence is greater than last received and within window
            if sequence > session.remote_sequence
                && sequence - session.remote_sequence < self.max_sequence_window
            {
                Ok(())
            } else if sequence <= session.remote_sequence {
                Err(SessionError::ReplayDetected)
            } else {
                Err(SessionError::SequenceOutOfWindow)
            }
        } else {
            Err(SessionError::SessionNotFound)
        }
    }

    /// Update remote sequence
    pub async fn update_remote_sequence(&self, session_id: &[u8; 8], sequence: u32) -> Result<(), SessionError> {
        let mut sessions = self.sessions.write().await;
        if let Some(session) = sessions.get_mut(session_id) {
            session.remote_sequence = sequence;
            session.update_activity();
            Ok(())
        } else {
            Err(SessionError::SessionNotFound)
        }
    }

    /// Remove a session
    pub async fn remove_session(&self, session_id: &[u8; 8]) -> Option<Session> {
        self.sessions.write().await.remove(session_id)
    }

    /// Get all session IDs
    pub async fn get_session_ids(&self) -> Vec<[u8; 8]> {
        self.sessions.read().await.keys().cloned().collect()
    }

    /// Clean up timed out sessions
    pub async fn cleanup_timed_out_sessions(&self) -> Vec<[u8; 8]> {
        let mut sessions = self.sessions.write().await;
        let now = Instant::now();
        let mut removed = Vec::new();

        sessions.retain(|id, session| {
            let timed_out = match session.state {
                SessionState::Init | SessionState::HandshakeSent => {
                    now.duration_since(session.created_at) > self.handshake_timeout
                }
                SessionState::Established => {
                    now.duration_since(session.last_activity) > self.heartbeat_timeout
                }
                SessionState::Closing | SessionState::Closed => true,
            };

            if timed_out {
                removed.push(*id);
            }
            !timed_out
        });

        removed
    }

    /// Get session count
    pub async fn session_count(&self) -> usize {
        self.sessions.read().await.len()
    }

    /// Get established session count
    pub async fn established_count(&self) -> usize {
        self.sessions
            .read()
            .await
            .values()
            .filter(|s| s.is_established())
            .count()
    }
}

impl Default for SessionManager {
    fn default() -> Self {
        Self::new()
    }
}

/// Session error types
#[derive(Debug, thiserror::Error)]
pub enum SessionError {
    #[error("Session not found")]
    SessionNotFound,

    #[error("Session already exists")]
    SessionAlreadyExists,

    #[error("Session not in correct state")]
    InvalidState,

    #[error("Replay attack detected")]
    ReplayDetected,

    #[error("Sequence out of window")]
    SequenceOutOfWindow,

    #[error("Crypto error: {0}")]
    CryptoError(String),
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn test_session_creation() {
        let manager = SessionManager::new();
        let key = crate::tunnel::crypto::generate_key();
        let id = manager
            .create_session("peer.example.com".to_string(), key)
            .await
            .unwrap();

        let session = manager.get_session(&id).await;
        assert!(session.is_some());
        assert_eq!(session.unwrap().peer_domain, "peer.example.com");
    }

    #[tokio::test]
    async fn test_session_removal() {
        let manager = SessionManager::new();
        let key = crate::tunnel::crypto::generate_key();
        let id = manager
            .create_session("peer.example.com".to_string(), key)
            .await
            .unwrap();

        let removed = manager.remove_session(&id).await;
        assert!(removed.is_some());

        let session = manager.get_session(&id).await;
        assert!(session.is_none());
    }

    #[tokio::test]
    async fn test_sequence_validation() {
        let manager = SessionManager::new();
        let key = crate::tunnel::crypto::generate_key();
        let id = manager
            .create_session("peer.example.com".to_string(), key)
            .await
            .unwrap();

        // First sequence should be valid
        assert!(manager.check_sequence(&id, 1).await.is_ok());
        manager.update_remote_sequence(&id, 1).await.unwrap();

        // Same sequence should be rejected (replay)
        assert!(matches!(
            manager.check_sequence(&id, 1).await,
            Err(SessionError::ReplayDetected)
        ));

        // Next sequence should be valid
        assert!(manager.check_sequence(&id, 2).await.is_ok());
    }
}
