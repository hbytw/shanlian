/**
 * ShanLian VPN SDK - C 包装层
 *
 * 提供对 Rust SDK 的 C 接口包装
 */

#include "shanlian.h"
#include <stdlib.h>
#include <string.h>

static ShanLianHandle g_handle = 0;
static ShanLianEventCallback g_callback = NULL;
static void* g_user_data = NULL;

static void default_event_callback(const ShanLianEvent* event) {
    if (g_callback) {
        g_callback(event);
    }
}

int shanlian_init(const ShanLianConfig* config, ShanLianHandle* handle) {
    if (!config || !handle) {
        return SHANLIAN_ERR_INVALID_PARAM;
    }

    int result = shanlian_init(config, handle);
    if (result == SHANLIAN_OK) {
        g_handle = *handle;
    }
    return result;
}

int shanlian_connect(ShanLianHandle handle) {
    return shanlian_connect(handle);
}

int shanlian_disconnect(ShanLianHandle handle) {
    return shanlian_disconnect(handle);
}

int shanlian_status(ShanLianHandle handle, ShanLianStatus* status) {
    return shanlian_status(handle, status);
}

int shanlian_set_event_callback(ShanLianHandle handle, ShanLianEventCallback callback) {
    int result = shanlian_set_event_callback(handle, callback);
    if (result == SHANLIAN_OK) {
        g_callback = callback;
    }
    return result;
}

int shanlian_poll(ShanLianHandle handle, int timeout_ms) {
    return shanlian_poll(handle, timeout_ms);
}

int shanlian_connect_peer(ShanLianHandle handle, const char* peer_id) {
    return shanlian_connect_peer(handle, peer_id);
}

int shanlian_disconnect_peer(ShanLianHandle handle, const char* peer_id) {
    return shanlian_disconnect_peer(handle, peer_id);
}

int shanlian_list_tunnels(ShanLianHandle handle, ShanLianTunnelInfo** tunnels, int* count) {
    return shanlian_list_tunnels(handle, tunnels, count);
}

void shanlian_free_tunnels(ShanLianHandle handle, ShanLianTunnelInfo* tunnels, int count) {
    shanlian_free_tunnels(handle, tunnels, count);
}

int shanlian_switch_tunnel(ShanLianHandle handle, ShanLianTunnelType type) {
    return shanlian_switch_tunnel(handle, type);
}

int shanlian_list_nodes(ShanLianHandle handle, ShanLianNodeQuality** nodes, int* count) {
    return shanlian_list_nodes(handle, nodes, count);
}

void shanlian_free_nodes(ShanLianHandle handle, ShanLianNodeQuality* nodes, int count) {
    shanlian_free_nodes(handle, nodes, count);
}

const char* shanlian_get_error(ShanLianHandle handle) {
    return shanlian_get_error(handle);
}

int shanlian_destroy(ShanLianHandle handle) {
    int result = shanlian_destroy(handle);
    if (result == SHANLIAN_OK) {
        if (g_handle == handle) {
            g_handle = 0;
            g_callback = NULL;
        }
    }
    return result;
}
