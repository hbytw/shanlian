/**
 * ShanLian VPN SDK
 *
 * C 语言 SDK 头文件
 * 提供 VPN 连接管理的 FFI 接口
 */

#ifndef SHANLIAN_SDK_H
#define SHANLIAN_SDK_H

#include <stdint.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

/**
 * SDK 版本
 */
#define SHANLIAN_SDK_VERSION "0.1.0"

/**
 * 句柄类型
 */
typedef intptr_t ShanLianHandle;

/**
 * 隧道类型
 */
typedef enum {
    SHANLIAN_TUNNEL_UDP  = 0,
    SHANLIAN_TUNNEL_TCP  = 1,
    SHANLIAN_TUNNEL_WSS  = 2,
    SHANLIAN_TUNNEL_MOON = 3,
} ShanLianTunnelType;

/**
 * 连接状态
 */
typedef enum {
    SHANLIAN_STATE_DISCONNECTED = 0,
    SHANLIAN_STATE_CONNECTING  = 1,
    SHANLIAN_STATE_CONNECTED   = 2,
    SHANLIAN_STATE_DISCONNECTING = 3,
    SHANLIAN_STATE_ERROR       = 4,
} ShanLianState;

/**
 * 隧道信息
 */
typedef struct {
    ShanLianTunnelType type;
    char moon_id[64];
    char local_ip[16];
    char remote_ip[16];
    uint16_t local_port;
    uint16_t remote_port;
    uint64_t bytes_sent;
    uint64_t bytes_recv;
    uint32_t rtt_ms;
    float loss_rate;
    bool active;
} ShanLianTunnelInfo;

/**
 * SDK 配置
 */
typedef struct {
    const char* controller_url;    // Controller 服务器地址
    const char* api_key;           // API Key
    const char* api_secret;        // API Secret
    const char* device_id;         // 设备 ID
    const char* device_name;       // 设备名称
    const char* identity_path;      // 私钥路径
    int heartbeat_interval;        // 心跳间隔（秒）
    int connection_timeout;        // 连接超时（秒）
} ShanLianConfig;

/**
 * 事件类型
 */
typedef enum {
    SHANLIAN_EVENT_PEER_ONLINE       = 0,
    SHANLIAN_EVENT_PEER_OFFLINE      = 1,
    SHANLIAN_EVENT_TUNNEL_ESTABLISHED = 2,
    SHANLIAN_EVENT_TUNNEL_CLOSED     = 3,
    SHANLIAN_EVENT_POLICY_UPDATED    = 4,
    SHANLIAN_EVENT_ERROR             = 5,
    SHANLIAN_EVENT_STATE_CHANGED     = 6,
} ShanLianEventType;

/**
 * 事件数据
 */
typedef struct {
    ShanLianEventType type;
    void* user_data;
    union {
        struct {
            const char* peer_id;
        } peer_online;
        struct {
            const char* peer_id;
        } peer_offline;
        struct {
            ShanLianTunnelType tunnel_type;
            const char* moon_id;
        } tunnel_established;
        struct {
            ShanLianTunnelType tunnel_type;
            const char* moon_id;
        } tunnel_closed;
        struct {
            const char* error_msg;
        } error;
        struct {
            ShanLianState old_state;
            ShanLianState new_state;
        } state_changed;
    } data;
} ShanLianEvent;

/**
 * 事件回调函数类型
 */
typedef void (*ShanLianEventCallback)(const ShanLianEvent* event);

/**
 * SDK 状态信息
 */
typedef struct {
    ShanLianState state;
    char assigned_ip[16];
    char network_cidr[8];
    char gateway_ip[16];
    uint64_t bytes_sent;
    uint64_t bytes_recv;
    uint64_t connected_time;
    int active_tunnels;
} ShanLianStatus;

/**
 * 节点质量信息
 */
typedef struct {
    char moon_id[64];
    char domain[256];
    uint16_t relay_port;
    uint16_t wss_port;
    uint16_t weight;
    bool online;
    uint32_t rtt_ms;
    float loss_rate;
} ShanLianNodeQuality;

/**
 * 错误码
 */
typedef enum {
    SHANLIAN_OK                  = 0,
    SHANLIAN_ERR_INVALID_PARAM   = -1,
    SHANLIAN_ERR_NOT_INITIALIZED = -2,
    SHANLIAN_ERR_ALREADY_INIT    = -3,
    SHANLIAN_ERR_CONNECT_FAILED  = -4,
    SHANLIAN_ERR_AUTH_FAILED     = -5,
    SHANLIAN_ERR_TIMEOUT         = -6,
    SHANLIAN_ERR_NETWORK         = -7,
    SHANLIAN_ERR_INTERNAL        = -8,
} ShanLianError;

/**
 * 初始化 SDK
 * @param config 配置信息
 * @param handle 输出句柄
 * @return SHANLIAN_OK 成功
 */
int shanlian_init(const ShanLianConfig* config, ShanLianHandle* handle);

/**
 * 设置事件回调
 * @param handle 句柄
 * @param callback 回调函数
 * @return SHANLIAN_OK 成功
 */
int shanlian_set_event_callback(ShanLianHandle handle, ShanLianEventCallback callback);

/**
 * 连接 VPN
 * @param handle 句柄
 * @return SHANLIAN_OK 成功
 */
int shanlian_connect(ShanLianHandle handle);

/**
 * 断开 VPN
 * @param handle 句柄
 * @return SHANLIAN_OK 成功
 */
int shanlian_disconnect(ShanLianHandle handle);

/**
 * 获取状态
 * @param handle 句柄
 * @param status 输出状态
 * @return SHANLIAN_OK 成功
 */
int shanlian_status(ShanLianHandle handle, ShanLianStatus* status);

/**
 * 轮询事件（用于单线程模式）
 * @param handle 句柄
 * @param timeout_ms 超时毫秒
 * @return SHANLIAN_OK 成功
 */
int shanlian_poll(ShanLianHandle handle, int timeout_ms);

/**
 * 连接指定节点
 * @param handle 句柄
 * @param peer_id 节点 ID
 * @return SHANLIAN_OK 成功
 */
int shanlian_connect_peer(ShanLianHandle handle, const char* peer_id);

/**
 * 断开指定节点
 * @param handle 句柄
 * @param peer_id 节点 ID
 * @return SHANLIAN_OK 成功
 */
int shanlian_disconnect_peer(ShanLianHandle handle, const char* peer_id);

/**
 * 获取隧道列表
 * @param handle 句柄
 * @param tunnels 输出隧道数组
 * @param count 输出隧道数量
 * @return SHANLIAN_OK 成功
 */
int shanlian_list_tunnels(ShanLianHandle handle, ShanLianTunnelInfo** tunnels, int* count);

/**
 * 释放隧道列表内存
 * @param handle 句柄
 * @param tunnels 隧道数组
 * @param count 隧道数量
 */
void shanlian_free_tunnels(ShanLianHandle handle, ShanLianTunnelInfo* tunnels, int count);

/**
 * 切换隧道类型
 * @param handle 句柄
 * @param type 隧道类型
 * @return SHANLIAN_OK 成功
 */
int shanlian_switch_tunnel(ShanLianHandle handle, ShanLianTunnelType type);

/**
 * 获取节点质量列表
 * @param handle 句柄
 * @param nodes 输出节点数组
 * @param count 输出节点数量
 * @return SHANLIAN_OK 成功
 */
int shanlian_list_nodes(ShanLianHandle handle, ShanLianNodeQuality** nodes, int* count);

/**
 * 释放节点列表内存
 * @param handle 句柄
 * @param nodes 节点数组
 * @param count 节点数量
 */
void shanlian_free_nodes(ShanLianHandle handle, ShanLianNodeQuality* nodes, int count);

/**
 * 获取错误信息
 * @param handle 句柄
 * @return 错误信息字符串
 */
const char* shanlian_get_error(ShanLianHandle handle);

/**
 * 销毁 SDK
 * @param handle 句柄
 * @return SHANLIAN_OK 成功
 */
int shanlian_destroy(ShanLianHandle handle);

#ifdef __cplusplus
}
#endif

#endif /* SHANLIAN_SDK_H */
