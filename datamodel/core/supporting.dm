type IPv4Address {
    octets@1: [4]u8
}

type IPv6Address {
    octets@1: [16]u8
}

type IPv4Prefix {
    address@1: IPv4Address
    prefix_length@2: u8 [max = 32]
}

type IPv6Prefix {
    address@1: IPv6Address
    prefix_length@2: u8 [max = 128]
}

type SecretRef {
    secret_id@1: Secret.id
    version@2: u32 [min = 1]
}

type ConfigRef {
    config_id@1: Config.id
    version@2: u32 [min = 1]
}

type AssetRef {
    asset_id@1: Asset.id
    version@2: u32 [min = 1]
}
