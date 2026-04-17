//! ChaCha20-Poly1305 加密
//!
//! 使用 chacha20poly1305 crate 实现加密

use anyhow::Result;
use chacha20poly1305::{
    aead::{Aead, KeyInit, OsRng},
    ChaCha20Poly1305, Nonce,
};
use hmac::{Hmac, Mac};
use rand::RngCore;
use sha2::{Digest, Sha256};

use crate::tunnel::slnx::SLNX_MAGIC;

type HmacSha256 = Hmac<Sha256>;

/// Crypto error types
#[derive(Debug, thiserror::Error)]
pub enum CryptoError {
    #[error("Encryption failed")]
    EncryptionFailed,

    #[error("Decryption failed")]
    DecryptionFailed,

    #[error("Invalid key size: expected 32 bytes, got {0}")]
    InvalidKeySize(usize),

    #[error("Key derivation failed")]
    KeyDerivationFailed,
}

/// ChaCha20-Poly1305 cipher
#[derive(Clone)]
pub struct ChaCha20Cipher {
    cipher: ChaCha20Poly1305,
}

impl ChaCha20Cipher {
    /// Create a new cipher with the given key
    pub fn new(key: &[u8]) -> Result<Self, CryptoError> {
        if key.len() != 32 {
            return Err(CryptoError::InvalidKeySize(key.len()));
        }
        let cipher = ChaCha20Poly1305::new_from_slice(key)
            .map_err(|_| CryptoError::KeyDerivationFailed)?;
        Ok(Self { cipher })
    }

    /// Encrypt data
    pub fn encrypt(&self, nonce: &[u8], plaintext: &[u8]) -> Result<Vec<u8>, CryptoError> {
        if nonce.len() != 12 {
            return Err(CryptoError::EncryptionFailed);
        }
        let nonce = Nonce::from_slice(nonce);
        self.cipher
            .encrypt(nonce, plaintext)
            .map_err(|_| CryptoError::EncryptionFailed)
    }

    /// Decrypt data
    pub fn decrypt(&self, nonce: &[u8], ciphertext: &[u8]) -> Result<Vec<u8>, CryptoError> {
        if nonce.len() != 12 {
            return Err(CryptoError::DecryptionFailed);
        }
        let nonce = Nonce::from_slice(nonce);
        self.cipher
            .decrypt(nonce, ciphertext)
            .map_err(|_| CryptoError::DecryptionFailed)
    }
}

/// Derive session key from pre-shared secret using HKDF-SHA256
pub fn derive_session_key(
    pre_shared_secret: &[u8],
    session_id: &[u8],
    salt: &[u8],
) -> Result<Vec<u8>, CryptoError> {
    if pre_shared_secret.len() != 32 {
        return Err(CryptoError::InvalidKeySize(pre_shared_secret.len()));
    }

    // HKDF-SHA256
    let mut okm = vec![0u8; 32];
    let mut info = Vec::with_capacity(16 + session_id.len());
    info.extend_from_slice(b"SLNX-v1-session");
    info.extend_from_slice(session_id);

    // Simplified HKDF: HMAC-SHA256(secret, info || 0x01)
    let mut mac = <HmacSha256 as Mac>::new_from_slice(salt)
        .map_err(|_| CryptoError::KeyDerivationFailed)?;
    mac.update(pre_shared_secret);
    mac.update(&info);
    mac.update(&[0x01]);

    let result = mac.finalize();
    okm.copy_from_slice(&result.into_bytes());

    Ok(okm)
}

/// Generate a random 32-byte key
pub fn generate_key() -> Vec<u8> {
    // Generate random nonce and derive key
    let mut key = vec![0u8; 32];
    OsRng.fill_bytes(&mut key);

    // Mix with magic constant
    let mut mac = <HmacSha256 as Mac>::new_from_slice(&key).unwrap();
    mac.update(&SLNX_MAGIC.to_be_bytes());
    let result = mac.finalize();
    key.copy_from_slice(&result.into_bytes()[..32]);

    key
}

/// Generate a random session ID (8 bytes)
pub fn generate_session_id() -> [u8; 8] {
    let mut id = [0u8; 8];
    OsRng.fill_bytes(&mut id);
    id
}

/// Generate a random nonce (12 bytes for ChaCha20-Poly1305)
pub fn generate_nonce() -> [u8; 12] {
    let mut nonce = [0u8; 12];
    OsRng.fill_bytes(&mut nonce);
    nonce
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_cipher_encrypt_decrypt() {
        let key = generate_key();
        let cipher = ChaCha20Cipher::new(&key).unwrap();
        let nonce = generate_nonce();
        let plaintext = b"Hello, World!";

        let ciphertext = cipher.encrypt(&nonce, plaintext).unwrap();
        let decrypted = cipher.decrypt(&nonce, &ciphertext).unwrap();

        assert_eq!(decrypted, plaintext);
    }

    #[test]
    fn test_derive_session_key() {
        let psk = generate_key();
        let session_id = [1, 2, 3, 4, 5, 6, 7, 8];
        let salt = [0u8; 32];

        let key1 = derive_session_key(&psk, &session_id, &salt).unwrap();
        let key2 = derive_session_key(&psk, &session_id, &salt).unwrap();

        assert_eq!(key1, key2);
        assert_eq!(key1.len(), 32);
    }

    #[test]
    fn test_different_session_different_key() {
        let psk = generate_key();
        let salt = [0u8; 32];

        let key1 = derive_session_key(&psk, &[1, 2, 3, 4, 5, 6, 7, 8], &salt).unwrap();
        let key2 = derive_session_key(&psk, &[8, 7, 6, 5, 4, 3, 2, 1], &salt).unwrap();

        assert_ne!(key1, key2);
    }
}
