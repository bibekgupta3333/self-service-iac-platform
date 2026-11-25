"""Custom Checkov checks (IACP_AWS_*) for the Self-Service IaC Platform.

Load with ``checkov --external-checks-dir policies/checkov``. Checkov imports
every ``.py`` file in a directory that has an ``__init__.py``; ``tests/`` has
none on purpose so the pytest suite is never loaded as checks.
"""
