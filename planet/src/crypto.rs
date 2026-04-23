//! 加密模块
//!
//! 使用 X25519 密钥交换和 ChaCha20-Poly1305 加密

use anyhow::Result;
use chacha20poly1305::{
    aead::{Aead, KeyInit, OsRng},
    ChaCha20Poly1305, Nonce,
};
use ed25519_dalek::{Signature, Signer, SigningKey, Verifier, VerifyingKey};
use std::path::PathBuf;
use x25519_dalek::{EphemeralSecret, PublicKey};
use hex;

/// 密钥对存储
pub struct KeyPair {
    pub signing_key: SigningKey,
    pub verifying_key: VerifyingKey,
}

/// 加密器
pub struct Crypto {
    shared_secret: [u8; 32],
}

impl KeyPair {
    /// 生成新的密钥对
    pub fn generate() -> Self {
        let signing_key = SigningKey::generate(&mut OsRng);
        let verifying_key = signing_key.verifying_key();
        Self {
            signing_key,
            verifying_key,
        }
    }

    /// 从文件加载密钥对
    pub fn load(path: &PathBuf) -> Result<Self> {
        if !path.exists() {
            return Err(anyhow::anyhow!("密钥文件不存在"));
        }
        let bytes = std::fs::read(path)?;
        let signing_key = SigningKey::from_bytes(bytes[..32].try_into()?);
        let verifying_key = signing_key.verifying_key();
        Ok(Self {
            signing_key,
            verifying_key,
        })
    }

    /// 保存密钥对到文件
    pub fn save(&self, path: &PathBuf) -> Result<()> {
        std::fs::create_dir_all(path.parent().unwrap_or(&PathBuf::from(".")))?;
        let bytes: [u8; 32] = self.signing_key.to_bytes();
        std::fs::write(path, bytes)?;
        Ok(())
    }

    /// 获取公钥
    pub fn public_key(&self) -> String {
        hex::encode(self.verifying_key.as_bytes())
    }

    /// 对数据签名
    pub fn sign(&self, data: &[u8]) -> Signature {
        self.signing_key.sign(data)
    }

    /// 验证签名
    pub fn verify(&self, data: &[u8], signature: &Signature) -> bool {
        self.verifying_key.verify(data, signature).is_ok()
    }
}

impl Crypto {
    /// 创建加密器
    pub fn new(shared_secret: [u8; 32]) -> Self {
        Self { shared_secret }
    }

    /// X25519 密钥交换
    pub fn x25519_key_exchange(
        local_private: EphemeralSecret,
        remote_public: &PublicKey,
    ) -> [u8; 32] {
        let shared_secret = local_private.diffie_hellman(remote_public);
        *shared_secret.as_bytes()
    }

    /// 加密数据
    pub fn encrypt(&self, plaintext: &[u8], nonce: &[u8; 12]) -> Result<Vec<u8>> {
        let cipher = ChaCha20Poly1305::new_from_slice(&self.shared_secret)
            .map_err(|e| anyhow::anyhow!("ChaCha20Poly1305 init failed: {:?}", e))?;
        let nonce = Nonce::from_slice(nonce);
        let ciphertext = cipher.encrypt(nonce, plaintext)
            .map_err(|e| anyhow::anyhow!("Encryption failed: {:?}", e))?;
        Ok(ciphertext)
    }

    /// 解密数据
    pub fn decrypt(&self, ciphertext: &[u8], nonce: &[u8; 12]) -> Result<Vec<u8>> {
        let cipher = ChaCha20Poly1305::new_from_slice(&self.shared_secret)
            .map_err(|e| anyhow::anyhow!("ChaCha20Poly1305 init failed: {:?}", e))?;
        let nonce = Nonce::from_slice(nonce);
        let plaintext = cipher.decrypt(nonce, ciphertext)
            .map_err(|e| anyhow::anyhow!("Decryption failed: {:?}", e))?;
        Ok(plaintext)
    }
}

/// 生成随机 Nonce
pub fn generate_nonce() -> [u8; 12] {
    use rand::Rng;
    let mut nonce = [0u8; 12];
    OsRng.fill(&mut nonce);
    nonce
}

/// HMAC-SHA256 签名
pub fn hmac_sha256(key: &[u8], data: &[u8]) -> Vec<u8> {
    use hmac::Mac;
    type HmacSha256 = hmac::Hmac<sha2::Sha256>;
    let mut mac = <HmacSha256 as Mac>::new_from_slice(key)
        .expect("HMAC key size should be valid");
    mac.update(data);
    hex::encode(mac.finalize().into_bytes()).into_bytes()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_keypair_generate() {
        let keypair = KeyPair::generate();
        assert_eq!(keypair.public_key().len(), 64); // hex encoded 32 bytes
    }

    #[test]
    fn test_sign_and_verify() {
        let keypair = KeyPair::generate();
        let data = b"Hello, VPN!";
        let signature = keypair.sign(data);
        assert!(keypair.verify(data, &signature));
    }

    #[test]
    fn test_sign_verify_fail() {
        let keypair = KeyPair::generate();
        let data = b"Hello, VPN!";
        let signature = keypair.sign(data);
        let wrong_data = b"Hello, VPN?";
        assert!(!keypair.verify(wrong_data, &signature));
    }

    #[test]
    fn test_crypto_encrypt_decrypt() {
        let keypair = KeyPair::generate();
        let crypto = Crypto::new(*keypair.verifying_key.as_bytes());
        let plaintext = b"Test VPN packet data";
        let nonce = generate_nonce();

        let ciphertext = crypto.encrypt(plaintext, &nonce).unwrap();
        let decrypted = crypto.decrypt(&ciphertext, &nonce).unwrap();

        assert_eq!(plaintext.to_vec(), decrypted);
    }

    #[test]
    fn test_crypto_wrong_nonce() {
        let keypair = KeyPair::generate();
        let crypto = Crypto::new(*keypair.verifying_key.as_bytes());
        let plaintext = b"Test VPN packet data";
        let nonce = generate_nonce();
        let wrong_nonce = generate_nonce();

        let ciphertext = crypto.encrypt(plaintext, &nonce).unwrap();
        let result = crypto.decrypt(&ciphertext, &wrong_nonce);
        assert!(result.is_err()); // Should fail with wrong nonce
    }

    #[test]
    fn test_hmac_sha256() {
        let key = b"secret_key";
        let data = b"test data";
        let hmac1 = hmac_sha256(key, data);
        let hmac2 = hmac_sha256(key, data);
        assert_eq!(hmac1, hmac2);
    }

    #[test]
    fn test_hmac_sha256_different_data() {
        let key = b"secret_key";
        let hmac1 = hmac_sha256(key, b"data1");
        let hmac2 = hmac_sha256(key, b"data2");
        assert_ne!(hmac1, hmac2);
    }

    #[test]
    fn test_x25519_key_exchange() {
        use x25519_dalek::{PublicKey, StaticSecret};
        use rand::SeedableRng;
        use rand::rngs::OsRng;

        let local_private = StaticSecret::new(OsRng);
        let local_public = PublicKey::from(&local_private);

        let remote_private = StaticSecret::new(OsRng);
        let remote_public = PublicKey::from(&remote_private);

        // Both should derive the same shared secret
        let secret1 = local_private.diffie_hellman(&remote_public);
        let secret2 = remote_private.diffie_hellman(&local_public);

        assert_eq!(*secret1.as_bytes(), *secret2.as_bytes());
    }

    #[test]
    fn test_generate_nonce() {
        let nonce1 = generate_nonce();
        let nonce2 = generate_nonce();
        assert_ne!(nonce1, nonce2); // Should be random
        assert_eq!(nonce1.len(), 12); // 12 bytes
    }
}
