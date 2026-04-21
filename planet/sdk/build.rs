//! ShanLian SDK 构建脚本
//!
//! 处理跨平台编译配置

fn main() {
    // 导出 C SDK 头文件路径
    println!("cargo:rerun-if-changed=sdk/include/shanlian.h");
    println!("cargo:rerun-if-changed=sdk/src/shanlian.c");

    // 根据目标平台配置
    #[cfg(target_os = "windows")]
    {
        println!("cargo:rustc-link-lib=dylib=ws2_32");
        println!("cargo:rustc-link-lib=dylib=userenv");
    }

    #[cfg(target_os = "linux")]
    {
        println!("cargo:rustc-link-lib=dylib=pthread");
        println!("cargo:rustc-link-lib=dylib=resolv");
    }

    #[cfg(target_os = "macos")]
    {
        println!("cargo:rustc-link-lib=dylib=System");
        println!("cargo:rustc-link-lib=framework=Security");
    }
}
