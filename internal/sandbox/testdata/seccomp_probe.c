#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <unistd.h>

static int check(const char *operation, long result, int denied) {
    if (denied ? result == -1 && errno == EPERM : result >= 0)
        return 0;
    fprintf(stderr, "%s: result=%ld errno=%d; expected %s\n",
            operation, result, errno, denied ? "EPERM" : "success");
    return 1;
}

int main(int argc, char **argv) {
    if (argc != 4)
        return 1;
    int denied = strcmp(argv[1], "confined") == 0;
    int writable = atoi(argv[3]);
    int fd = open(argv[2], O_RDONLY);
    if (check("open for reading", fd, 0))
        return 1;
    char content[32] = {0};
    ssize_t n = read(fd, content, sizeof(content) - 1);
    close(fd);
    if (n != 9 || strcmp(content, "preserved") != 0) {
        fprintf(stderr, "allowed read: %zd bytes, %s\n", n, content);
        return 1;
    }

    fd = open(argv[2], O_RDONLY | O_TRUNC);
    int failed = check("open with O_TRUNC", fd, denied);
    if (fd >= 0)
        close(fd);
    failed |= check("ftruncate inherited descriptor", ftruncate(writable, 0), denied);
    failed |= check("write inherited descriptor", write(writable, "changed", 7), denied);

    fd = socket(AF_INET, SOCK_STREAM, 0);
    failed |= check("socket", fd, denied);
    if (fd >= 0)
        close(fd);

    pid_t child = fork();
    if (child == 0)
        _exit(0);
    failed |= check("fork", child, denied);
    if (child > 0) {
        int status;
        if (waitpid(child, &status, 0) != child || !WIFEXITED(status) || WEXITSTATUS(status) != 0)
            return 1;
    }
    return failed;
}
