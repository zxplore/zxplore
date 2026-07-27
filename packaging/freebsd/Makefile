PORTNAME=	zxplor
DISTVERSION=	0.1.0
CATEGORIES=	sysutils
MAINTAINER=	hello@zxplor.ca
COMMENT=	Direct interface to your ZFS primitives — not a dashboard
WWW=		https://zxplor.ca

LICENSE=	BSD3CLAUSE

RUN_DEPENDS=	bash:shells/bash
USES=		gmake
NO_ARCH=	yes
PLIST_FILES=	bin/zxplor bin/zxplor-api bin/zxplor-txn

do-install:
	${MAKE_CMD} -C ${WRKSRC} DESTDIR=${STAGEDIR} PREFIX=${PREFIX} install

.include <bsd.port.mk>
