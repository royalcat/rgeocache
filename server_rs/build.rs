fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Only compile v2 proto (has all geometry types + V2Header + zones).
    // CacheMetadata from v1 is parsed manually (3 simple fields).
    prost_build::Config::new().compile_protos(&["proto/cache_v2.proto"], &["proto"])?;

    // Compile v1 proto (for CacheMetadata parsing).
    prost_build::Config::new().compile_protos(&["proto/cache.proto"], &["proto"])?;

    println!("cargo:rerun-if-changed=proto/cache_v2.proto");
    println!("cargo:rerun-if-changed=proto/cache_v1.proto");

    Ok(())
}
