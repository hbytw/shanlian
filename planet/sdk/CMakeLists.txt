cmake_minimum_required(VERSION 3.18)
project(ShanLianSDK C)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)

# SDK 版本
set(SDK_VERSION "0.1.0")

# 源文件
set(SOURCES
    src/shanlian.c
)

# 头文件
set(HEADERS
    include/shanlian.h
)

# 库名称
add_library(shanlian-sdk SHARED ${SOURCES} ${HEADERS})

# 包含目录
target_include_directories(shanlian-sdk PUBLIC
    ${CMAKE_CURRENT_SOURCE_DIR}/include
)

# 平台特定配置
if(WIN32)
    target_link_libraries(shanlian-sdk ws2_32 userenv)
    target_compile_definitions(shanlian-sdk PRIVATE SHANLIAN_PLATFORM_WINDOWS=1)
elseif(ANDROID)
    target_link_libraries(shanlian-sdk log android)
    target_compile_definitions(shanlian-sdk PRIVATE SHANLIAN_PLATFORM_ANDROID=1)
elseif(APPLE)
    find_library(FOUNDATION Foundation)
    find_library(SECURITY Security)
    target_link_libraries(shanlian-sdk ${FOUNDATION} ${SECURITY})
    target_compile_definitions(shanlian-sdk PRIVATE SHANLIAN_PLATFORM_APPLE=1)
else()
    target_link_libraries(shanlian-sdk pthread resolv dl)
    target_compile_definitions(shanlian-sdk PRIVATE SHANLIAN_PLATFORM_LINUX=1)
endif()

# 版本信息
set_target_properties(shanlian-sdk PROPERTIES
    VERSION ${SDK_VERSION}
    SOVERSION 1
)

# 安装目标
install(TARGETS shanlian-sdk
    LIBRARY DESTINATION lib
    ARCHIVE DESTINATION lib
    RUNTIME DESTINATION bin
)

install(FILES ${HEADERS}
    DESTINATION include
)
