enum FilePermission {
    ReadWrite@1
    ReadOnly@2
    ReadExecute@3
}

type Space@8 root {
    id@1: u64 [key, max = 65535]
    name@2: string [min_length = 1]
    laws {
        space_name {
            unique (name)
        }
    }
}
