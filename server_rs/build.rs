fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Only compile v2 proto (has all geometry types + V2Header + zones).
    // CacheMetadata from v1 is parsed manually (3 simple fields).
    prost_build::Config::new()
        .compile_protos(
            &["../cachesaver/save/v2/proto/cache_v2.proto"],
            &["../cachesaver/save/v2/proto"],
        )?;

    println!("cargo:rerun-if-changed=../cachesaver/save/v2/proto/cache_v2.proto");
    Ok(())
}
