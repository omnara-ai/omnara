-- +goose Up

ALTER TABLE machines VALIDATE CONSTRAINT machines_delete_reason_check;
