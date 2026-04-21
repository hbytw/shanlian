/**
 * ShanLian SDK 使用示例
 */

#include "shanlian.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* 事件回调函数 */
void on_event(const ShanLianEvent* event) {
    printf("[Event] type=%d\n", event->event_type);

    switch (event->event_type) {
        case SHANLIAN_EVENT_STATE_CHANGED:
            printf("  State changed: %d -> %d\n",
                   event->data.state_changed.old_state,
                   event->data.state_changed.new_state);
            break;

        case SHANLIAN_EVENT_TUNNEL_ESTABLISHED:
            printf("  Tunnel established: type=%d, moon=%s\n",
                   event->data.tunnel_established.tunnel_type,
                   event->data.tunnel_established.moon_id);
            break;

        case SHANLIAN_EVENT_ERROR:
            printf("  Error: %s\n", event->data.error.error_msg);
            break;

        default:
            break;
    }
}

int main(int argc, char* argv[]) {
    ShanLianHandle handle;
    ShanLianError err;
    ShanLianConfig config;
    ShanLianStatus status;
    ShanLianTunnelInfo* tunnels = NULL;
    int tunnel_count = 0;

    printf("ShanLian SDK Example\n");
    printf("SDK Version: %s\n\n", SHANLIAN_SDK_VERSION);

    /* 配置 SDK */
    memset(&config, 0, sizeof(config));
    config.controller_url = "https://controller.shanlian.io";
    config.api_key = "your_api_key";
    config.api_secret = "your_api_secret";
    config.device_id = "DID_example_device_001";
    config.device_name = "Example Device";
    config.heartbeat_interval = 30;
    config.connection_timeout = 30;

    /* 初始化 SDK */
    err = shanlian_init(&config, &handle);
    if (err != SHANLIAN_OK) {
        printf("Failed to init SDK: %d\n", err);
        return 1;
    }
    printf("SDK initialized, handle=%ld\n", (long)handle);

    /* 设置事件回调 */
    err = shanlian_set_event_callback(handle, on_event);
    if (err != SHANLIAN_OK) {
        printf("Failed to set callback: %d\n", err);
        shanlian_destroy(handle);
        return 1;
    }

    /* 连接 VPN */
    printf("Connecting VPN...\n");
    err = shanlian_connect(handle);
    if (err != SHANLIAN_OK) {
        printf("Failed to connect: %d\n", err);
        shanlian_destroy(handle);
        return 1;
    }

    /* 获取状态 */
    err = shanlian_status(handle, &status);
    if (err == SHANLIAN_OK) {
        printf("Status: state=%d, IP=%s\n", status.state, status.assigned_ip);
    }

    /* 获取隧道列表 */
    err = shanlian_list_tunnels(handle, &tunnels, &tunnel_count);
    if (err == SHANLIAN_OK) {
        printf("Active tunnels: %d\n", tunnel_count);
        for (int i = 0; i < tunnel_count; i++) {
            printf("  [%d] type=%d, moon=%s, active=%d\n",
                   i, tunnels[i].tunnel_type, tunnels[i].moon_id, tunnels[i].active);
        }
        shanlian_free_tunnels(handle, tunnels, tunnel_count);
    }

    /* 主循环 - 轮询事件 */
    printf("\nPolling events for 10 seconds...\n");
    for (int i = 0; i < 20; i++) {
        shanlian_poll(handle, 500);

        /* 检查状态 */
        err = shanlian_status(handle, &status);
        if (err == SHANLIAN_OK && status.state == SHANLIAN_STATE_CONNECTED) {
            printf(".");
        }
    }

    printf("\n\nDisconnecting...\n");
    err = shanlian_disconnect(handle);
    if (err != SHANLIAN_OK) {
        printf("Failed to disconnect: %d\n", err);
    }

    /* 销毁 SDK */
    err = shanlian_destroy(handle);
    if (err != SHANLIAN_OK) {
        printf("Failed to destroy SDK: %d\n", err);
        return 1;
    }

    printf("Done.\n");
    return 0;
}
