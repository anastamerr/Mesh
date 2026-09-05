import { CanActivate, ExecutionContext, Inject, Injectable, UnauthorizedException } from '@nestjs/common';
import { CONFIG, Config } from '../config';
import { bearer, matchesSecret } from './tokens';

@Injectable()
export class AdminGuard implements CanActivate {
  constructor(@Inject(CONFIG) private readonly config: Config) {}
  canActivate(context: ExecutionContext): boolean {
    const request = context.switchToHttp().getRequest<{ headers: { authorization?: string } }>();
    if (!matchesSecret(bearer(request.headers.authorization), this.config.adminKey)) {
      throw new UnauthorizedException('Invalid administrator credential');
    }
    return true;
  }
}
