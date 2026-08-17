export const mockSecretRefs = [
    {id: 301, name: 'db-url', version: 3, spaceId: 1},
    {id: 302, name: 'smtp-password', version: 1, spaceId: 1},
    {id: 303, name: 'stripe-api-key', version: 5, spaceId: 1},
];

export const mockConfigRefs = [
    {id: 401, name: 'otel-endpoint', version: 2, spaceId: 1},
    {id: 402, name: 'feature-flags', version: 7, spaceId: 1},
];

export const mockSpaces = [{id: 1, name: 'global'}];

const typical = [
    {key: 'DATABASE_URL', type: 'secret', secretId: 301},
    {key: 'DATABASE_POOL_SIZE', value: '10'},
    {key: 'DATABASE_TLS_ENABLED', value: 'true'},
    {key: 'CACHE_HOST', value: 'redis.internal'},
    {key: 'CACHE_PORT', value: '6379'},
    {key: 'CACHE_ENABLED', value: 'false'},
    {key: 'FEATURE_SIGNUP_ENABLED', value: 'true'},
    {key: 'FEATURE_BILLING_ENABLED', value: 'false'},
    {key: 'OTEL_EXPORTER_ENDPOINT', type: 'config', configId: 401},
    {key: 'OTEL_SERVICE_NAME', value: 'api'},
    {key: 'METRICS_ENABLED', value: '1'},
    {key: 'PORT', value: '8080'},
    {key: 'LOG_LEVEL', value: 'debug'},
    {key: 'DEBUG', value: 'false'},
];

const many = [
    ...typical,
    {key: 'SMTP_HOST', value: 'smtp.postmark.example'},
    {key: 'SMTP_PORT', value: '587'},
    {key: 'SMTP_PASSWORD', type: 'secret', secretId: 302},
    {key: 'SMTP_TLS_ENABLED', value: 'yes'},
    {key: 'AWS_REGION', value: 'ap-southeast-2'},
    {key: 'AWS_ACCESS_KEY_ID', value: 'AKIA-fixture'},
    {key: 'S3_BUCKET', value: 'opsagent-backups'},
    {key: 'S3_ENDPOINT', value: 'https://s3.example'},
    {key: 'STRIPE_API_KEY', type: 'secret', secretId: 303},
    {key: 'STRIPE_WEBHOOKS_ENABLED', value: 'on'},
    {key: 'TRACING_SAMPLE_RATE', value: '0.25'},
];

export const scenarioEnvVars = {
    typical: () => typical.map(spec => ({...spec})),
    many: () => many.map(spec => ({...spec})),
    empty: () => [],
};
