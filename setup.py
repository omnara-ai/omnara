from setuptools import setup
from setuptools.dist import Distribution

try:
    from wheel.bdist_wheel import bdist_wheel as _bdist_wheel  # type: ignore

    class bdist_wheel(_bdist_wheel):  # type: ignore
        def finalize_options(self):
            super().finalize_options()
            # Mark wheel as non-pure so it gets a platform tag and can carry binaries.
            self.root_is_pure = False
except Exception:  # pragma: no cover - wheel may not be available in some envs
    bdist_wheel = None  # type: ignore


class BinaryDistribution(Distribution):
    def has_ext_modules(self):  # type: ignore[override]
        # Tell setuptools/wheel that this distribution contains non-pure (binary) artifacts.
        return True


setup(
    distclass=BinaryDistribution,
    cmdclass={"bdist_wheel": bdist_wheel} if bdist_wheel else {},
)
