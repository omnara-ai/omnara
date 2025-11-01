#!/usr/bin/env python3
"""
Generate RSA key pair for JWT signing.
Run this once to generate keys, then add them to your .env file.
"""

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import rsa


def generate_rsa_key_pair():
    """Generate RSA key pair for JWT signing"""

    # Generate private key
    private_key = rsa.generate_private_key(
        public_exponent=65537,
        # Using 2048-bit keys for secure JWT signing (industry standard)
        key_size=2048,
    )

    # Get public key
    public_key = private_key.public_key()

    # Serialize private key
    private_pem = private_key.private_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PrivateFormat.PKCS8,
        encryption_algorithm=serialization.NoEncryption(),
    )

    # Serialize public key
    public_pem = public_key.public_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PublicFormat.SubjectPublicKeyInfo,
    )

    return private_pem.decode("utf-8"), public_pem.decode("utf-8")


if __name__ == "__main__":
    print("Generating RSA key pair for JWT signing...\n")

    private_key, public_key = generate_rsa_key_pair()

    print("=" * 80)
    print("Add these to your .env file:")
    print("=" * 80)

    print("\n# JWT Signing Keys")
    print("JWT_PRIVATE_KEY=" + repr(private_key))
    print("JWT_PUBLIC_KEY=" + repr(public_key))

    print("\n" + "=" * 80)
    print("IMPORTANT: Keep the public and private keys secure!")
    print("- Add it to .env (not .env.example)")
    print("- Never commit keys to git")
    print("=" * 80)
