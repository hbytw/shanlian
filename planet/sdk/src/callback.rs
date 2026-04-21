//! 事件回调模块

use std::sync::mpsc::{channel, Receiver, Sender};

pub enum SdkEvent {
    PeerOnline(String),
    PeerOffline(String),
    TunnelEstablished {
        tunnel_type: u8,
        moon_id: String,
    },
    TunnelClosed {
        tunnel_type: u8,
        moon_id: String,
    },
    PolicyUpdated,
    Error(String),
    StateChanged {
        old_state: u8,
        new_state: u8,
    },
}

pub struct EventChannel {
    sender: Sender<SdkEvent>,
    receiver: Receiver<SdkEvent>,
}

impl EventChannel {
    pub fn new() -> Self {
        let (sender, receiver) = channel();
        Self { sender, receiver }
    }

    pub fn send(&self, event: SdkEvent) {
        let _ = self.sender.send(event);
    }

    pub fn try_recv(&self) -> Option<SdkEvent> {
        self.receiver.try_recv().ok()
    }

    pub fn recv(&self) -> Option<SdkEvent> {
        self.receiver.recv().ok()
    }
}

impl Default for EventChannel {
    fn default() -> Self {
        Self::new()
    }
}
