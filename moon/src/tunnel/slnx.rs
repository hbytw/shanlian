//! SLNX 协议编解码
//!
//! 帧格式：
//! | Magic (4B) | Version (1B) | Flags (1B) | Type (1B) |
//! | Session ID (8B) | Sequence # (4B) | Length (2B) | Checksum (4B) | Payload |

use anyhow::Result;
use crc32fast::Hasher as Crc32Hasher;

use crate::tunnel::crypto::CryptoError;

/// SLNX Magic bytes: 0x534C4E58 ('SLNX')
pub const SLNX_MAGIC: u32 = 0x534C4E58;

/// SLNX Version
pub const SLNX_VERSION: u8 = 1;

/// SLNX Flags
#[derive(Debug, Clone, Copy, PartialEq)]
#[repr(u8)]
pub enum SlnxFlag {
    Handshake = 0x01,
    Data = 0x02,
    Heartbeat = 0x04,
    Close = 0x08,
}

impl SlnxFlag {
    pub fn from_bits(bits: u8) -> Vec<SlnxFlag> {
        let mut flags = Vec::new();
        if bits & 0x01 != 0 {
            flags.push(SlnxFlag::Handshake);
        }
        if bits & 0x02 != 0 {
            flags.push(SlnxFlag::Data);
        }
        if bits & 0x04 != 0 {
            flags.push(SlnxFlag::Heartbeat);
        }
        if bits & 0x08 != 0 {
            flags.push(SlnxFlag::Close);
        }
        flags
    }

    pub fn bits(&self) -> u8 {
        *self as u8
    }
}

/// SLNX Frame Types
#[derive(Debug, Clone, Copy, PartialEq)]
#[repr(u8)]
pub enum SlnxType {
    IpPacket = 0x01,
    TunnelControl = 0x02,
    Fec = 0x03,
}

impl SlnxType {
    pub fn bits(&self) -> u8 {
        *self as u8
    }
}

/// SLNX Header (25 bytes total)
#[derive(Debug, Clone)]
pub struct SlnxHeader {
    pub magic: u32,
    pub version: u8,
    pub flags: u8,
    pub frame_type: u8,
    pub session_id: [u8; 8],
    pub sequence: u32,
    pub length: u16,
    pub checksum: u32,
}

impl SlnxHeader {
    pub const HEADER_SIZE: usize = 25;

    /// Parse header from bytes
    pub fn parse(data: &[u8]) -> Result<Self, SlnxError> {
        if data.len() < Self::HEADER_SIZE {
            return Err(SlnxError::InvalidHeaderLength(data.len()));
        }

        let magic = u32::from_be_bytes([data[0], data[1], data[2], data[3]]);
        if magic != SLNX_MAGIC {
            return Err(SlnxError::InvalidMagic(magic, SLNX_MAGIC));
        }

        let version = data[4];
        if version != SLNX_VERSION {
            return Err(SlnxError::InvalidVersion(version, SLNX_VERSION));
        }

        let flags = data[5];
        let frame_type = data[6];
        let session_id: [u8; 8] = data[7..15].try_into().unwrap();
        let sequence = u32::from_be_bytes([data[15], data[16], data[17], data[18]]);
        let length = u16::from_be_bytes([data[19], data[20]]);
        let checksum = u32::from_be_bytes([data[21], data[22], data[23], data[24]]);

        Ok(Self {
            magic,
            version,
            flags,
            frame_type,
            session_id,
            sequence,
            length,
            checksum,
        })
    }

    /// Serialize header to bytes
    pub fn to_bytes(&self) -> Vec<u8> {
        let mut buf = Vec::with_capacity(Self::HEADER_SIZE);
        buf.extend_from_slice(&self.magic.to_be_bytes());
        buf.push(self.version);
        buf.push(self.flags);
        buf.push(self.frame_type);
        buf.extend_from_slice(&self.session_id);
        buf.extend_from_slice(&self.sequence.to_be_bytes());
        buf.extend_from_slice(&self.length.to_be_bytes());
        buf.extend_from_slice(&self.checksum.to_be_bytes());
        buf
    }
}

/// SLNX Frame
#[derive(Debug, Clone)]
pub struct SlnxFrame {
    pub header: SlnxHeader,
    pub payload: Vec<u8>,
}

impl SlnxFrame {
    /// Parse a complete frame from bytes
    pub fn parse(data: &[u8]) -> Result<Self, SlnxError> {
        let header = SlnxHeader::parse(data)?;

        if data.len() < SlnxHeader::HEADER_SIZE + header.length as usize {
            return Err(SlnxError::PayloadLengthMismatch {
                expected: header.length as usize,
                actual: data.len() - SlnxHeader::HEADER_SIZE,
            });
        }

        let payload_start = SlnxHeader::HEADER_SIZE;
        let payload = data[payload_start..payload_start + header.length as usize].to_vec();

        // Verify checksum
        let calculated_checksum = crc32c(&payload);
        if calculated_checksum != header.checksum {
            return Err(SlnxError::ChecksumMismatch {
                expected: header.checksum,
                actual: calculated_checksum,
            });
        }

        Ok(Self { header, payload })
    }

    /// Create a new frame
    pub fn new(
        flags: u8,
        frame_type: u8,
        session_id: [u8; 8],
        sequence: u32,
        payload: Vec<u8>,
    ) -> Self {
        let checksum = crc32c(&payload);
        let header = SlnxHeader {
            magic: SLNX_MAGIC,
            version: SLNX_VERSION,
            flags,
            frame_type,
            session_id,
            sequence,
            length: payload.len() as u16,
            checksum,
        };
        Self { header, payload }
    }

    /// Serialize frame to bytes
    pub fn to_bytes(&self) -> Vec<u8> {
        let mut buf = self.header.to_bytes();
        buf.extend_from_slice(&self.payload);
        buf
    }
}

/// CRC32C checksum
pub fn crc32c(data: &[u8]) -> u32 {
    let mut hasher = Crc32Hasher::new();
    hasher.update(data);
    hasher.finalize()
}

/// SLNX Error types
#[derive(Debug, thiserror::Error)]
pub enum SlnxError {
    #[error("Invalid header length: {0}")]
    InvalidHeaderLength(usize),

    #[error("Invalid magic: 0x{0:08X}, expected 0x{1:08X}")]
    InvalidMagic(u32, u32),

    #[error("Invalid version: {0}, expected {1}")]
    InvalidVersion(u8, u8),

    #[error("Payload length mismatch: expected {expected}, got {actual}")]
    PayloadLengthMismatch { expected: usize, actual: usize },

    #[error("Checksum mismatch: expected 0x{expected:08X}, got 0x{actual:08X}")]
    ChecksumMismatch { expected: u32, actual: u32 },
}

/// Build a handshake SYN frame
pub fn build_handshake_syn(session_id: [u8; 8], sequence: u32, payload: Vec<u8>) -> SlnxFrame {
    SlnxFrame::new(SlnxFlag::Handshake.bits(), SlnxType::TunnelControl.bits(), session_id, sequence, payload)
}

/// Build a data frame
pub fn build_data_frame(session_id: [u8; 8], sequence: u32, payload: Vec<u8>) -> SlnxFrame {
    SlnxFrame::new(SlnxFlag::Data.bits(), SlnxType::IpPacket.bits(), session_id, sequence, payload)
}

/// Build a heartbeat frame
pub fn build_heartbeat_frame(session_id: [u8; 8], sequence: u32) -> SlnxFrame {
    SlnxFrame::new(SlnxFlag::Heartbeat.bits(), SlnxType::TunnelControl.bits(), session_id, sequence, Vec::new())
}

/// Build a close frame
pub fn build_close_frame(session_id: [u8; 8], sequence: u32) -> SlnxFrame {
    SlnxFrame::new(SlnxFlag::Close.bits(), SlnxType::TunnelControl.bits(), session_id, sequence, Vec::new())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_crc32c() {
        let data = b"hello world";
        let crc1 = crc32c(data);
        let crc2 = crc32c(data);
        // Verify consistency
        assert_eq!(crc1, crc2);
        // Verify non-zero result (sanity check)
        assert!(crc1 != 0);
    }

    #[test]
    fn test_frame_roundtrip() {
        let session_id = [1, 2, 3, 4, 5, 6, 7, 8];
        let payload = b"test payload".to_vec();
        let frame = SlnxFrame::new(
            SlnxFlag::Data.bits(),
            SlnxType::IpPacket.bits(),
            session_id,
            42,
            payload.clone(),
        );

        let encoded = frame.to_bytes();
        let decoded = SlnxFrame::parse(&encoded).unwrap();

        assert_eq!(decoded.header.session_id, session_id);
        assert_eq!(decoded.header.sequence, 42);
        assert_eq!(decoded.payload, payload);
    }

    #[test]
    fn test_handshake_frames() {
        let session_id = [0u8; 8];
        let syn = build_handshake_syn(session_id, 0, vec![1, 2, 3]);
        assert!(syn.header.flags & SlnxFlag::Handshake.bits() != 0);
    }
}
