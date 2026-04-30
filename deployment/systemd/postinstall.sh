#!/bin/sh
systemctl daemon-reload
systemctl enable hydra
systemctl restart hydra