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
        let cipher = ChaCha20Poly1305::new_from_slice(&self.shared_secret)?;
        let nonce = Nonce::from_slice(nonce);
        let ciphertext = cipher.encrypt(nonce, plaintext)?;
        Ok(ciphertext)
    }

    /// 解密数据
    pub fn decrypt(&self, ciphertext: &[u8], nonce: &[u8; 12]) -> Result<Vec<u8>> {
        let cipher = ChaCha20Poly1305::new_from_slice(&self.shared_secret)?;
        let nonce = Nonce::from_slice(nonce);
        let plaintext = cipher.decrypt(nonce, ciphertext)?;
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
    use hmac::{Hmac, Mac};
    type HmacSha256 = Hmac<sha2::Sha256>;
    let mut mac = HmacSha256::new_from_slice(key).unwrap();
    mac.update(data);
    hex::encode(mac.finalize().into_bytes()).into_bytes()
}
