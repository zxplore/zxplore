PORTNAME=	zxplore
DISTVERSION=	0.1.0
CATEGORIES=	sysutils
MAINTAINER=	admin@zxplore.dev
COMMENT=	Direct interface to your ZFS primitives — not a dashboard
WWW=		https://zxplore.dev

LICENSE=	BSD3CLAUSE

USES=		gmake go
PLIST_FILES=	bin/zxplore \
		bin/zxplore-tui \
		bin/zxplore-api \
		bin/zxplore-txn \
		share/man/man1/zxplore.1.gz

do-install:
	${MAKE_CMD} -C ${WRKSRC} DESTDIR=${STAGEDIR} PREFIX=${PREFIX} install

.include <bsd.port.mk>
