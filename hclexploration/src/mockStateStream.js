// A consolidated mock of the snapshots received from PostV1StateStream.
// Field names match the generated JavaScript protobuf model.
export const mockStateStreamState = {
    spacesSnapshot: {
        items: [
            {id: 1, name: "production"},
            {id: 2, name: "staging"},
            {id: 3, name: "development"},
        ],
    },
    nodesSnapshot: {
        items: [
            {id: 1, name: "worker_01", identifier: "worker-01", roles: [0]},
            {id: 2, name: "worker_02", identifier: "worker-02", roles: [1]},
            {id: 3, name: "worker_03", identifier: "worker-03", roles: [1]},
        ],
    },
    secretsSnapshot: {
        items: [
            {id: 99, name: "database.password", spaceId: 1, version: 4},
            {id: 103, name: "github_token", spaceId: 1, version: 2},
            {id: 118, name: "webhook_signing_key", spaceId: 1, version: 7},
            {id: 201, name: "sandbox_api_token", spaceId: 2, version: 1},
        ],
    },
    userConfigsSnapshot: {
        items: [
            {id: 42, name: "database.url", spaceId: 1, version: 5},
            {id: 46, name: "queue_name", spaceId: 1, version: 3},
            {id: 51, name: "log_level", spaceId: 1, version: 8},
            {id: 204, name: "sandbox_database_url", spaceId: 2, version: 1},
        ],
    },
    assetsSnapshot: {
        items: [
            {id: 24, key: "payments_settings", spaceId: 1, version: 3, format: "toml"},
            {id: 31, key: "worker_settings", spaceId: 1, version: 6, format: "toml"},
            {id: 35, key: "application.license", spaceId: 1, version: 1, format: "text"},
            {id: 207, key: "staging_proxy_config", spaceId: 2, version: 2, format: "nginx"},
        ],
    },
    deploymentsSnapshot: {
        items: [
            {config: {id: 17, configId: {spaceId: 1, name: "redis.cache"}, version: 12}, status: {state: "running"}},
            {config: {id: 21, configId: {spaceId: 1, name: "metrics_gateway"}, version: 8}, status: {state: "running"}},
            {config: {id: 28, configId: {spaceId: 1, name: "report.archive"}, version: 4}, status: {state: "stopped"}},
            {config: {id: 211, configId: {spaceId: 2, name: "staging_redis"}, version: 2}, status: {state: "running"}},
        ],
    },
};

function selectedSpace(state, document) {
    const spaces = state.spacesSnapshot?.items || [];
    const name = /(?:^|\n)\s*space\s*=\s*space\(\s*"([^"]+)"\s*\)/m.exec(document)?.[1];
    return spaces.find(space => space.name === name)
        || spaces.find(space => space.name === "production")
        || spaces[0];
}

function stateItem(key, id, detail, version, spaceId) {
    return {key, id, detail, version, spaceId};
}

export function referenceCatalogForDocument(document, state = mockStateStreamState) {
    const spaces = (state.spacesSnapshot?.items || []).filter(item => !item.deleted);
    const nodes = (state.nodesSnapshot?.items || []).filter(item => !item.deleted);
    const space = selectedSpace(state, document);
    const inSelectedSpace = item => !item.deleted && item.spaceId === space?.id;

    return {
        space: spaces.map(item => stateItem(item.name, item.id, `Space ${item.id}`)),
        node: nodes.map(item => stateItem(item.name, item.id, item.identifier || "Cluster node")),
        secret: (state.secretsSnapshot?.items || []).filter(inSelectedSpace)
            .map(item => stateItem(item.name, item.id, `Secret version ${item.version}`, item.version)),
        config: (state.userConfigsSnapshot?.items || []).filter(inSelectedSpace)
            .map(item => stateItem(item.name, item.id, `Config version ${item.version}`, item.version)),
        asset: (state.assetsSnapshot?.items || []).filter(inSelectedSpace)
            .map(item => stateItem(item.key, item.id, `${item.format || "file"} asset, version ${item.version}`, item.version)),
        deployment: (state.deploymentsSnapshot?.items || [])
            .filter(item => !item.config?.deleted)
            .map(item => stateItem(
                item.config.configId.name,
                item.config.id,
                `Deployment version ${item.config.version}`,
                undefined,
                item.config.configId.spaceId,
            )),
    };
}

export function selectedSpaceName(document, state = mockStateStreamState) {
    return selectedSpace(state, document)?.name || "none";
}
