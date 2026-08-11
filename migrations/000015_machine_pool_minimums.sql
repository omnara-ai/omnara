-- +goose Up

ALTER TABLE machine_pools
    ADD COLUMN min_machine_cpu integer;

ALTER TABLE machine_pools
    ADD COLUMN min_machine_memory_mb integer;

ALTER TABLE machine_pools
    ADD CONSTRAINT machine_pools_min_machine_cpu_check
        CHECK (min_machine_cpu IS NULL OR min_machine_cpu >= 0),
    ADD CONSTRAINT machine_pools_min_machine_memory_mb_check
        CHECK (min_machine_memory_mb IS NULL OR min_machine_memory_mb >= 0),
    ADD CONSTRAINT machine_pools_min_max_machine_cpu_check
        CHECK (min_machine_cpu IS NULL OR max_machine_cpu IS NULL OR min_machine_cpu <= max_machine_cpu),
    ADD CONSTRAINT machine_pools_min_max_machine_memory_mb_check
        CHECK (min_machine_memory_mb IS NULL OR max_machine_memory_mb IS NULL OR min_machine_memory_mb <= max_machine_memory_mb),
    ADD CONSTRAINT machine_pools_default_min_machine_cpu_check
        CHECK (min_machine_cpu IS NULL OR default_machine_cpu IS NULL OR default_machine_cpu >= min_machine_cpu),
    ADD CONSTRAINT machine_pools_default_min_machine_memory_mb_check
        CHECK (min_machine_memory_mb IS NULL OR default_machine_memory_mb IS NULL OR default_machine_memory_mb >= min_machine_memory_mb);

ALTER TABLE project_machine_pool_grants
    ADD COLUMN min_machine_cpu integer;

ALTER TABLE project_machine_pool_grants
    ADD COLUMN min_machine_memory_mb integer;

ALTER TABLE project_machine_pool_grants
    ADD CONSTRAINT project_machine_pool_grants_min_machine_cpu_check
        CHECK (min_machine_cpu IS NULL OR min_machine_cpu >= 0),
    ADD CONSTRAINT project_machine_pool_grants_min_machine_memory_mb_check
        CHECK (min_machine_memory_mb IS NULL OR min_machine_memory_mb >= 0),
    ADD CONSTRAINT project_machine_pool_grants_min_max_machine_cpu_check
        CHECK (min_machine_cpu IS NULL OR max_machine_cpu IS NULL OR min_machine_cpu <= max_machine_cpu),
    ADD CONSTRAINT project_machine_pool_grants_min_max_machine_memory_mb_check
        CHECK (min_machine_memory_mb IS NULL OR max_machine_memory_mb IS NULL OR min_machine_memory_mb <= max_machine_memory_mb),
    ADD CONSTRAINT project_machine_pool_grants_default_min_machine_cpu_check
        CHECK (min_machine_cpu IS NULL OR default_machine_cpu IS NULL OR default_machine_cpu >= min_machine_cpu),
    ADD CONSTRAINT project_machine_pool_grants_default_min_machine_memory_check
        CHECK (min_machine_memory_mb IS NULL OR default_machine_memory_mb IS NULL OR default_machine_memory_mb >= min_machine_memory_mb);
