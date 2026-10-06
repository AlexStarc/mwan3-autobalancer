# The SDK's CURDIR is its build root. Resolve the real feed Makefile before
# includes change MAKEFILE_LIST; the complete repository is mounted as /feed.
AB_PACKAGE_DIR:=$(dir $(realpath $(lastword $(MAKEFILE_LIST))))
AB_SOURCE_DIR:=$(realpath $(AB_PACKAGE_DIR)/../..)

include $(TOPDIR)/rules.mk

PKG_NAME:=mwan3-autobalancer
PKG_VERSION:=0.1.0
PKG_RELEASE:=2
PKG_LICENSE:=MIT
PKG_LICENSE_FILES:=LICENSE
PKG_MAINTAINER:=Alex Starchenko
PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_ASLR_PIE:=0
PKG_ASLR_PIE_REGULAR:=0
PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)

GO_PKG:=github.com/AlexStarc/mwan3-autobalancer
GO_PKG_BUILD_PKG:=$(GO_PKG)/cmd/mwan3-autobalancer

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

# golang-package.mk defaults to CGO and external linking. This controller has
# no C dependencies and must have no dynamic loader on the ARM64 target.
GO_PKG_TARGET_VARS:=$(subst CGO_ENABLED=1,CGO_ENABLED=0,$(GO_PKG_TARGET_VARS))
GO_PKG_DEFAULT_LDFLAGS=-buildid '$(SOURCE_DATE_EPOCH)' -linkmode internal
GO_PKG_ENABLE_PIE:=

define Package/mwan3-autobalancer
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Bounded mwan3 bandwidth calibration and recovery
  DEPENDS:=$(GO_ARCH_DEPENDS) +mwan3 +curl +jsonfilter +jshn +rpcd +iptables-zz-legacy
endef

define Package/mwan3-autobalancer/description
  Foreground IPv4 controller with a shell-only stock-policy watchdog.
  Supported baseline: OpenWrt 24.10.8, mwan3 2.11.16-r5, legacy iptables.
  Installs in observation mode with an empty probe URL.
endef

define Package/mwan3-autobalancer/conffiles
/etc/config/mwan3_autobalancer
/etc/mwan3-autobalancer/budgets
endef

define Build/Prepare
	$(call Build/Prepare/Default)
	$(CP) $(AB_SOURCE_DIR)/go.mod $(AB_SOURCE_DIR)/LICENSE $(PKG_BUILD_DIR)/
	$(CP) $(AB_SOURCE_DIR)/cmd $(AB_SOURCE_DIR)/internal $(PKG_BUILD_DIR)/
endef

define Package/mwan3-autobalancer/install
	$(call GoPackage/Package/Install/Bin,$(1))
	$(INSTALL_DIR) $(1)/etc/config $(1)/etc/init.d
	$(INSTALL_DIR) -m0700 $(1)/etc/mwan3-autobalancer
	$(INSTALL_CONF) $(AB_SOURCE_DIR)/openwrt/config/mwan3_autobalancer $(1)/etc/config/mwan3_autobalancer
	$(INSTALL_CONF) $(AB_SOURCE_DIR)/openwrt/config/budgets $(1)/etc/mwan3-autobalancer/budgets
	$(INSTALL_BIN) $(AB_SOURCE_DIR)/openwrt/init.d/mwan3-autobalancer $(1)/etc/init.d/mwan3-autobalancer
	$(INSTALL_DIR) $(1)/usr/libexec/mwan3-autobalancer $(1)/usr/libexec/rpcd
	$(INSTALL_BIN) $(AB_SOURCE_DIR)/openwrt/libexec/bounded $(AB_SOURCE_DIR)/openwrt/libexec/watchdog $(AB_SOURCE_DIR)/openwrt/libexec/restore $(AB_SOURCE_DIR)/openwrt/libexec/stock-restore $(AB_SOURCE_DIR)/openwrt/libexec/stock-iptables $(AB_SOURCE_DIR)/openwrt/libexec/stop-instances $(AB_SOURCE_DIR)/openwrt/libexec/fallback-rollback $(1)/usr/libexec/mwan3-autobalancer/
	$(INSTALL_DATA) $(AB_SOURCE_DIR)/openwrt/libexec/common.sh $(AB_SOURCE_DIR)/openwrt/libexec/leaf.awk $(1)/usr/libexec/mwan3-autobalancer/
	$(INSTALL_BIN) $(AB_SOURCE_DIR)/openwrt/rpcd/mwan3.autobalancer $(1)/usr/libexec/rpcd/mwan3.autobalancer
endef

define Package/mwan3-autobalancer/postinst
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] && exit 0
umask 077
mkdir -p /etc/mwan3-autobalancer
chmod 700 /etc/mwan3-autobalancer
/etc/init.d/mwan3-autobalancer enable
/etc/init.d/mwan3-autobalancer restart
# Register the new exec object without changing the router's network service.
/etc/init.d/rpcd reload
endef

define Package/mwan3-autobalancer/prerm
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] && exit 0
# Keep stock mwan3/curl dependencies and persistent budgets. Stop restores the
# selected stock leaf before the independent recovery helper is removed.
/etc/init.d/mwan3-autobalancer disable
/etc/init.d/mwan3-autobalancer stop || exit 1
endef

define Package/mwan3-autobalancer/postrm
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] && exit 0
/etc/init.d/rpcd reload
endef

$(eval $(call BuildPackage,mwan3-autobalancer))
